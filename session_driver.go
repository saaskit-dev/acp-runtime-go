package acpruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

type SessionDriver interface {
	Close(context.Context) error
	Delete(context.Context) error
	Logout(context.Context) error
	CancelTurn(context.Context, string) (bool, error)
	SetAgentMode(context.Context, string) error
	SetAgentConfigOption(context.Context, string, any) error
	StartTurn(context.Context, RuntimePrompt) TurnHandle
	Snapshot() RuntimeSnapshot
	Status() string
	Capabilities() RuntimeCapabilities
	Diagnostics() RuntimeDiagnostics
	Metadata() RuntimeSessionMetadata
	ThreadEntries() []ThreadEntry
	ToolCalls() []ToolCallSnapshot
	Operations() []Operation
	PermissionRequests() []PermissionRequestSnapshot
}

type acpSessionDriver struct {
	connection *Connection
	dispose    func(context.Context) error
	agent      Agent
	cwd        string
	mcpServers []MCPServer
	profile    AgentProfile
	hooks      RuntimeHooks

	sessionID string

	mu           sync.RWMutex
	status       string
	capabilities RuntimeCapabilities
	diagnostics  RuntimeDiagnostics
	metadata     RuntimeSessionMetadata
	thread       []ThreadEntry
	toolCalls    map[string]ToolCallSnapshot
	operations   map[string]Operation
	permissions  map[string]PermissionRequestSnapshot
	currentTurn  *activeTurn
	turnSeq      int
	rawConfig    map[string]any
	queuePolicy  QueuePolicy
	// read-model caps (resolved defaults; 0 should not appear after construction)
	maxThread      int
	maxToolCalls   int
	maxPermissions int
}

type activeTurn struct {
	id         string
	events     chan TurnEvent
	completion chan TurnResult
	outputText strings.Builder
	// dropIntermediate, when true, suppresses intermediate TurnEvents (text
	// chunks, tool/plan updates) so the consumer only sees the final
	// completion. Driven by QueuePolicy.Delivery == "drop". State mutation in
	// handleSessionUpdate (toolCalls/operations maps, outputText) still occurs
	// regardless, so the read model stays consistent.
	dropIntermediate bool
	// finishOnce ensures Close/Delete racing with runPrompt only terminalizes
	// the turn once, so completion/events channels are closed exactly once.
	finishOnce sync.Once
	startedAt  time.Time
	// cancelTimer is set by CancelTurn; stopped in finishTurn.
	cancelTimer *time.Timer
}

type sessionBootstrap struct {
	Agent              Agent
	CWD                string
	MCPServers         []MCPServer
	Connection         *Connection
	Dispose            func(context.Context) error
	InitializeResponse InitializeResponse
	SessionResponse    NewSessionResponse
	Profile            AgentProfile
	QueuePolicy        QueuePolicy
	Hooks              RuntimeHooks
	ReadModelLimits    ReadModelLimits
}

func newACPSessionDriver(bootstrap sessionBootstrap) *acpSessionDriver {
	maxThread, maxToolCalls, maxPermissions := resolveReadModelLimits(bootstrap.ReadModelLimits)
	driver := &acpSessionDriver{
		connection:     bootstrap.Connection,
		dispose:        bootstrap.Dispose,
		agent:          bootstrap.Agent,
		cwd:            bootstrap.CWD,
		mcpServers:     append([]MCPServer(nil), bootstrap.MCPServers...),
		profile:        bootstrap.Profile,
		hooks:          bootstrap.Hooks,
		sessionID:      bootstrap.SessionResponse.SessionID,
		status:         "ready",
		capabilities:   capabilitiesFromInitialize(bootstrap.InitializeResponse),
		metadata:       metadataFromSessionResponse(bootstrap.SessionResponse),
		toolCalls:      map[string]ToolCallSnapshot{},
		operations:     map[string]Operation{},
		permissions:    map[string]PermissionRequestSnapshot{},
		rawConfig:      rawConfigFromMetadata(metadataFromSessionResponse(bootstrap.SessionResponse)),
		queuePolicy:    bootstrap.QueuePolicy,
		maxThread:      maxThread,
		maxToolCalls:   maxToolCalls,
		maxPermissions: maxPermissions,
	}
	driver.metadata.SessionID = bootstrap.SessionResponse.SessionID
	bootstrap.Connection.SetSessionUpdateHandler(func(ctx context.Context, notification SessionNotification) {
		driver.handleSessionUpdate(notification)
	})
	bootstrap.Connection.SetPermissionObserver(func(req PermissionRequest, decision PermissionDecision) {
		driver.recordPermission(req, decision)
	})
	return driver
}

func resolveReadModelLimits(limits ReadModelLimits) (thread, tools, permissions int) {
	thread = limits.MaxThreadEntries
	if thread == 0 {
		thread = DefaultMaxThreadEntries
	}
	tools = limits.MaxToolCallEntries
	if tools == 0 {
		tools = DefaultMaxToolCallEntries
	}
	permissions = limits.MaxPermissionEntries
	if permissions == 0 {
		permissions = DefaultMaxPermissionEntries
	}
	return thread, tools, permissions
}

// recordPermission stores a permission request and its host-decided outcome in
// the read model. It is invoked by the Connection whenever the agent sends a
// session/request_permission request (reverse direction). Keyed by ToolCallID
// when present, otherwise by a synthetic id so multiple unattributed requests
// do not collide.
func (d *acpSessionDriver) recordPermission(req PermissionRequest, decision PermissionDecision) {
	d.mu.Lock()
	defer d.mu.Unlock()
	id := req.ToolCallID
	if id == "" {
		d.turnSeq++
		id = fmt.Sprintf("permission-%d", d.turnSeq)
	}
	d.permissions[id] = PermissionRequestSnapshot{
		ID:        id,
		Phase:     "decided",
		Operation: decision.Outcome,
		Request:   req,
	}
	d.prunePermissionsLocked()
}

func capabilitiesFromInitialize(resp InitializeResponse) RuntimeCapabilities {
	caps := resp.AgentCapabilities
	return RuntimeCapabilities{
		CanLoadSession:     caps.LoadSession,
		CanListSessions:    caps.SessionCapabilities.List != nil,
		CanForkSession:     caps.SessionCapabilities.Fork != nil,
		CanResumeSession:   caps.SessionCapabilities.Resume != nil,
		CanCloseSession:    caps.SessionCapabilities.Close != nil,
		PromptCapabilities: caps.PromptCapabilities,
	}
}

func metadataFromSessionResponse(resp NewSessionResponse) RuntimeSessionMetadata {
	metadata := RuntimeSessionMetadata{SessionID: resp.SessionID}
	if resp.Modes != nil {
		metadata.CurrentModeID = resp.Modes.CurrentModeID
		for _, mode := range resp.Modes.AvailableModes {
			metadata.AgentModes = append(metadata.AgentModes, RuntimeAgentMode{ID: mode.ID, Name: mode.Name, Description: mode.Description})
		}
	}
	if resp.Models != nil {
		for _, model := range resp.Models.AvailableModels {
			metadata.AgentModels = append(metadata.AgentModels, RuntimeAgentModel{ID: model.ID, Name: model.Name, Description: model.Description})
		}
	}
	for _, option := range resp.ConfigOptions {
		metadata.AgentConfigOptions = append(metadata.AgentConfigOptions, runtimeConfigOptionFromACP(option))
	}
	metadata.AvailableCommands = append([]AvailableCommand(nil), resp.AvailableCommands...)
	return metadata
}

func runtimeConfigOptionFromACP(option SessionConfigOption) RuntimeAgentConfigOption {
	description := ""
	if option.Description != nil {
		description = *option.Description
	}
	category := ""
	if option.Category != nil {
		category = *option.Category
	}
	return RuntimeAgentConfigOption{
		Type:        option.Type,
		ID:          option.ID,
		Name:        option.Name,
		Description: description,
		Category:    category,
		Value:       option.Value,
		Options:     option.Options,
	}
}

func rawConfigFromMetadata(metadata RuntimeSessionMetadata) map[string]any {
	rawConfig := make(map[string]any, len(metadata.AgentConfigOptions)+1)
	if metadata.CurrentModeID != "" {
		rawConfig["mode"] = metadata.CurrentModeID
	}
	for _, option := range metadata.AgentConfigOptions {
		if option.ID != "" && option.Value != nil {
			rawConfig[option.ID] = option.Value
		}
	}
	return rawConfig
}

// replaceConfigOptionsLocked replaces the runtime read model with the full
// provider snapshot. The caller must hold d.mu for writing.
func (d *acpSessionDriver) replaceConfigOptionsLocked(options []SessionConfigOption) {
	next := make([]RuntimeAgentConfigOption, 0, len(options))
	for _, option := range options {
		next = append(next, runtimeConfigOptionFromACP(option))
	}
	d.metadata.AgentConfigOptions = next
	d.rawConfig = rawConfigFromMetadata(d.metadata)
}

func (d *acpSessionDriver) Close(ctx context.Context) error {
	active := d.beginClose()
	d.finishInFlightTurn(ctx, active, "session.close")
	_ = d.connection.CloseSession(ctx, CloseSessionRequest{SessionID: d.sessionID})
	var disposeErr error
	if d.dispose != nil {
		disposeErr = d.dispose(ctx)
	}
	d.emitHookSession(RuntimeSessionEvent{Type: "closed", SessionID: d.sessionID, AgentType: d.agent.Type, Err: disposeErr})
	return disposeErr
}

// Delete issues session/delete (removes the session from the agent's persistent
// storage) and then tears down the connection like Close. Agents that do not
// implement session/delete will return an error, which the caller may treat as
// non-fatal (the session is still closed locally).
func (d *acpSessionDriver) Delete(ctx context.Context) error {
	active := d.beginClose()
	d.finishInFlightTurn(ctx, active, "session.delete")
	deleteErr := d.connection.DeleteSession(ctx, DeleteSessionRequest{SessionID: d.sessionID})
	var disposeErr error
	if d.dispose != nil {
		disposeErr = d.dispose(ctx)
	}
	err := errors.Join(deleteErr, disposeErr)
	d.emitHookSession(RuntimeSessionEvent{Type: "deleted", SessionID: d.sessionID, AgentType: d.agent.Type, Err: err})
	return err
}

// beginClose marks the driver closed and returns any in-flight turn. Callers
// must terminalize that turn so consumers waiting on Completion do not hang.
func (d *acpSessionDriver) beginClose() *activeTurn {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.status == "closed" {
		return nil
	}
	d.status = "closed"
	return d.currentTurn
}

func (d *acpSessionDriver) finishInFlightTurn(ctx context.Context, active *activeTurn, op string) {
	if active == nil {
		return
	}
	// Best-effort cooperative cancel before local terminalization. Agents may
	// ignore session/cancel; finishTurn still unblocks host consumers.
	_ = d.connection.Cancel(ctx, CancelRequest{SessionID: d.sessionID})
	d.finishTurn(active, TurnCompletion{}, &RuntimeError{
		Kind: ErrorSessionClosed,
		Op:   op,
		Msg:  "session closed during turn",
	})
}

// Logout asks the agent to discard cached credentials (logout). Unlike
// Close/Delete it does not end the session; callers may follow it with Close.
// Agents that have not implemented logout (common in current adapter versions)
// return JSON-RPC method-not-found, which we treat as success so callers do not
// need to special-case older agents.
func (d *acpSessionDriver) Logout(ctx context.Context) error {
	if err := d.connection.Logout(ctx, LogoutRequest{}); err != nil && !isMethodNotImplemented(err) {
		return err
	}
	return nil
}

// cancelTurnLocalTimeout is how long CancelTurn waits for the agent to end the
// prompt before locally terminalizing the turn. Agents may ignore session/cancel.
const cancelTurnLocalTimeout = 15 * time.Second

func (d *acpSessionDriver) CancelTurn(ctx context.Context, turnID string) (bool, error) {
	d.mu.Lock()
	active := d.currentTurn
	if active == nil || active.id != turnID {
		d.mu.Unlock()
		return false, nil
	}
	// Arm a local timeout once per cancel so repeated CancelTurn calls do not
	// stack timers. The timer is cleared in finishTurn.
	if active.cancelTimer == nil {
		turn := active
		active.cancelTimer = time.AfterFunc(cancelTurnLocalTimeout, func() {
			d.finishTurn(turn, TurnCompletion{}, &RuntimeError{
				Kind: ErrorTurnCancelled,
				Op:   "session.cancel_turn",
				Msg:  "turn cancelled locally after agent did not stop",
			})
		})
	}
	d.mu.Unlock()
	if err := d.connection.Cancel(ctx, CancelRequest{SessionID: d.sessionID}); err != nil {
		return false, err
	}
	d.emitHookTurn(RuntimeTurnEvent{Type: "cancelled", SessionID: d.sessionID, TurnID: turnID})
	return true, nil
}

func (d *acpSessionDriver) SetAgentMode(ctx context.Context, modeID string) error {
	if err := d.connection.SetSessionMode(ctx, SetSessionModeRequest{SessionID: d.sessionID, ModeID: modeID}); err != nil {
		return err
	}
	d.mu.Lock()
	d.metadata.CurrentModeID = modeID
	d.rawConfig["mode"] = modeID
	d.mu.Unlock()
	return nil
}

func (d *acpSessionDriver) SetAgentConfigOption(ctx context.Context, id string, value any) error {
	resp, err := d.connection.SetSessionConfigOption(ctx, SetSessionConfigOptionRequest{SessionID: d.sessionID, OptionID: id, Value: value})
	if err != nil {
		return err
	}
	d.mu.Lock()
	d.rawConfig[id] = value
	if resp.ConfigOptions != nil {
		d.replaceConfigOptionsLocked(*resp.ConfigOptions)
	} else {
		// Older providers returned an empty response. Preserve their legacy
		// behavior while waiting for a config_option_update notification.
		for i := range d.metadata.AgentConfigOptions {
			if d.metadata.AgentConfigOptions[i].ID == id {
				d.metadata.AgentConfigOptions[i].Value = value
			}
		}
	}
	d.mu.Unlock()
	return nil
}

func (d *acpSessionDriver) StartTurn(ctx context.Context, prompt RuntimePrompt) TurnHandle {
	d.mu.Lock()
	if d.status == "closed" {
		d.mu.Unlock()
		return closedTurnHandle(&RuntimeError{Kind: ErrorSessionClosed, Op: "session.start_turn", Msg: "session is closed"})
	}
	// Single-flight: concurrent turns would race on currentTurn routing and the
	// shared read model. Reject instead of silently overwriting the active turn.
	if d.currentTurn != nil {
		d.mu.Unlock()
		d.emitHookTurn(RuntimeTurnEvent{Type: "coalesced", SessionID: d.sessionID, Err: &RuntimeError{Kind: ErrorTurnCoalesced, Op: "session.start_turn", Msg: "a turn is already running"}})
		return closedTurnHandle(&RuntimeError{
			Kind: ErrorTurnCoalesced,
			Op:   "session.start_turn",
			Msg:  "a turn is already running",
		})
	}
	d.turnSeq++
	turnID := fmt.Sprintf("turn-%d", d.turnSeq)
	now := time.Now()
	active := &activeTurn{
		id:               turnID,
		events:           make(chan TurnEvent, 64),
		completion:       make(chan TurnResult, 1),
		dropIntermediate: d.queuePolicy.Delivery == "drop",
		startedAt:        now,
	}
	d.currentTurn = active
	d.status = "running"
	d.thread = append(d.thread, ThreadEntry{ID: turnID + "-user", Kind: "user_message", Status: "completed", Text: promptText(prompt), CreatedAt: now, UpdatedAt: now})
	d.pruneThreadLocked()
	d.mu.Unlock()
	d.emitTurnEvent(active, TurnEvent{Type: "started", TurnID: turnID})
	d.emitHookTurn(RuntimeTurnEvent{Type: "started", SessionID: d.sessionID, TurnID: turnID})
	go d.runPrompt(ctx, active, prompt)
	return TurnHandle{TurnID: turnID, Events: active.events, Completion: active.completion}
}

func (d *acpSessionDriver) runPrompt(ctx context.Context, active *activeTurn, prompt RuntimePrompt) {
	resp, err := d.connection.Prompt(ctx, PromptRequest{SessionID: d.sessionID, Prompt: mapPrompt(prompt)})
	if err != nil {
		d.finishTurn(active, TurnCompletion{}, err)
		return
	}
	d.mu.RLock()
	outputText := active.outputText.String()
	d.mu.RUnlock()
	completion := TurnCompletion{TurnID: active.id, OutputText: outputText, StopReason: resp.StopReason, Usage: resp.Usage}
	d.finishTurn(active, completion, nil)
}

// emitTurnEvent non-blockingly delivers an intermediate event. A full buffer
// drops the event so session/update handling never stalls the peer read loop.
// Read-model mutation happens before emit and is unaffected by drops.
func (d *acpSessionDriver) emitTurnEvent(active *activeTurn, event TurnEvent) {
	if active == nil {
		return
	}
	select {
	case active.events <- event:
	default:
		if d.hooks.OnEventDrop != nil {
			d.hooks.OnEventDrop(RuntimeEventDrop{SessionID: d.sessionID, TurnID: active.id, EventType: event.Type})
		}
	}
}

func (d *acpSessionDriver) finishTurn(active *activeTurn, completion TurnCompletion, err error) {
	if active == nil {
		return
	}
	active.finishOnce.Do(func() {
		d.mu.Lock()
		if active.cancelTimer != nil {
			active.cancelTimer.Stop()
			active.cancelTimer = nil
		}
		if d.currentTurn == active {
			d.currentTurn = nil
			if d.status != "closed" {
				d.status = "ready"
			}
		}
		if err == nil {
			now := time.Now()
			d.thread = append(d.thread, ThreadEntry{ID: active.id + "-assistant", Kind: "assistant_message", Status: "completed", Text: completion.OutputText, CreatedAt: now, UpdatedAt: now})
			d.pruneThreadLocked()
		}
		d.mu.Unlock()

		// Completion first: Session.Run only waits on Completion and may never
		// drain Events. Delivering completion before the terminal event keeps
		// Run unblocked even when the events buffer is full of intermediates.
		if err != nil {
			select {
			case active.completion <- TurnResult{Err: err}:
			default:
			}
			select {
			case active.events <- TurnEvent{Type: "failed", TurnID: active.id, Error: err}:
			default:
			}
		} else {
			select {
			case active.completion <- TurnResult{Completion: completion}:
			default:
			}
			select {
			case active.events <- TurnEvent{Type: "completed", TurnID: active.id, Completion: &completion}:
			default:
			}
		}
		close(active.events)
		close(active.completion)

		eventType := "completed"
		if err != nil {
			eventType = "failed"
			var runtimeErr *RuntimeError
			if errors.As(err, &runtimeErr) && runtimeErr.Kind == ErrorTurnCancelled {
				eventType = "cancelled"
			}
		}
		var duration time.Duration
		if !active.startedAt.IsZero() {
			duration = time.Since(active.startedAt)
		}
		d.emitHookTurn(RuntimeTurnEvent{
			Type:      eventType,
			SessionID: d.sessionID,
			TurnID:    active.id,
			Duration:  duration,
			Err:       err,
		})
	})
}

func (d *acpSessionDriver) emitHookTurn(event RuntimeTurnEvent) {
	if d.hooks.OnTurnEvent != nil {
		d.hooks.OnTurnEvent(event)
	}
}

func (d *acpSessionDriver) emitHookSession(event RuntimeSessionEvent) {
	if d.hooks.OnSessionEvent != nil {
		d.hooks.OnSessionEvent(event)
	}
}

// pruneThreadLocked keeps only the most recent MaxThreadEntries. Caller holds d.mu.
func (d *acpSessionDriver) pruneThreadLocked() {
	max := d.maxThread
	if max == 0 {
		max = DefaultMaxThreadEntries
	}
	if max < 0 || len(d.thread) <= max {
		return
	}
	// Drop oldest entries; retain the trailing window.
	drop := len(d.thread) - max
	copy(d.thread, d.thread[drop:])
	d.thread = d.thread[:max]
}

// pruneToolCallsLocked drops completed/failed tool calls when over the cap,
// then drops arbitrary oldest keys if still over. Caller holds d.mu.
func (d *acpSessionDriver) pruneToolCallsLocked() {
	max := d.maxToolCalls
	if max == 0 {
		max = DefaultMaxToolCallEntries
	}
	if max < 0 || len(d.toolCalls) <= max {
		return
	}
	for id, snapshot := range d.toolCalls {
		if len(d.toolCalls) <= max {
			return
		}
		if snapshot.Status == "completed" || snapshot.Status == "failed" {
			delete(d.toolCalls, id)
			delete(d.operations, id)
		}
	}
	for id := range d.toolCalls {
		if len(d.toolCalls) <= max {
			return
		}
		delete(d.toolCalls, id)
		delete(d.operations, id)
	}
}

func (d *acpSessionDriver) prunePermissionsLocked() {
	max := d.maxPermissions
	if max == 0 {
		max = DefaultMaxPermissionEntries
	}
	if max < 0 || len(d.permissions) <= max {
		return
	}
	for id := range d.permissions {
		if len(d.permissions) <= max {
			return
		}
		delete(d.permissions, id)
	}
}

func (d *acpSessionDriver) handleSessionUpdate(notification SessionNotification) {
	if notification.SessionID != "" && notification.SessionID != d.sessionID {
		return
	}
	update := notification.Update
	d.mu.Lock()
	active := d.currentTurn
	switch update.SessionUpdate {
	case "agent_message_chunk":
		if active != nil {
			active.outputText.WriteString(sessionUpdateText(update))
		}
	case "current_mode_update":
		if update.CurrentModeID != "" {
			d.metadata.CurrentModeID = update.CurrentModeID
			d.rawConfig["mode"] = update.CurrentModeID
		}
	case "config_option_update":
		d.replaceConfigOptionsLocked(update.ConfigOptions)
	case "session_info_update":
		if update.Title != nil {
			d.metadata.Title = *update.Title
		}
		if update.UpdatedAt != nil {
			d.metadata.UpdatedAt = *update.UpdatedAt
		}
	case "available_commands_update":
		d.metadata.AvailableCommands = append([]AvailableCommand(nil), update.AvailableCommands...)
	case "tool_call":
		id := update.ToolCallID
		if id != "" {
			title := ""
			if update.Title != nil {
				title = *update.Title
			}
			kind := ""
			if update.Kind != nil {
				kind = *update.Kind
			}
			status := ""
			if update.Status != nil {
				status = *update.Status
			}
			snapshot := ToolCallSnapshot{ID: id, Title: title, Kind: kind, Status: status, Content: []ContentBlock(update.Content), RawInput: update.RawInput, RawOutput: update.RawOutput, UpdatedAt: time.Now()}
			d.toolCalls[id] = snapshot
			d.operations[id] = Operation{ID: id, Kind: d.profile.MapOperationKind(kind), Phase: operationPhase(status), Title: title, Target: inferOperationTarget(update), UpdatedAt: time.Now()}
			d.pruneToolCallsLocked()
		}
	case "tool_call_update":
		id := update.ToolCallID
		if id != "" {
			snapshot := d.toolCalls[id]
			if update.Title != nil {
				snapshot.Title = *update.Title
			}
			if update.Kind != nil {
				snapshot.Kind = *update.Kind
			}
			if update.Status != nil {
				snapshot.Status = *update.Status
			}
			if update.Content != nil {
				snapshot.Content = []ContentBlock(update.Content)
			}
			if len(update.RawInput) > 0 {
				snapshot.RawInput = update.RawInput
			}
			if len(update.RawOutput) > 0 {
				snapshot.RawOutput = update.RawOutput
			}
			snapshot.UpdatedAt = time.Now()
			d.toolCalls[id] = snapshot
			op := d.operations[id]
			op.Phase = operationPhase(snapshot.Status)
			op.UpdatedAt = snapshot.UpdatedAt
			d.operations[id] = op
			d.pruneToolCallsLocked()
		}
	}
	d.mu.Unlock()
	if active == nil || active.dropIntermediate {
		// "drop" delivery still mutates the read model above; only the event
		// stream is suppressed. nil active means no in-flight turn.
		return
	}
	switch update.SessionUpdate {
	case "agent_message_chunk":
		// Text was already folded into outputText above; recompute once for the event.
		d.emitTurnEvent(active, TurnEvent{Type: "text", TurnID: active.id, Text: sessionUpdateText(update)})
	case "agent_thought_chunk":
		d.emitTurnEvent(active, TurnEvent{Type: "thinking", TurnID: active.id, Thinking: sessionUpdateText(update)})
	case "plan":
		d.emitTurnEvent(active, TurnEvent{Type: "plan_updated", TurnID: active.id, Plan: update.Entries})
	case "usage_update":
		d.emitTurnEvent(active, TurnEvent{Type: "usage_updated", TurnID: active.id, Usage: update.Usage})
	case "session_info_update", "available_commands_update":
		d.emitTurnEvent(active, TurnEvent{Type: "metadata_updated", TurnID: active.id})
	case "tool_call", "tool_call_update":
		if update.ToolCallID != "" {
			d.mu.RLock()
			tool := d.toolCalls[update.ToolCallID]
			op := d.operations[update.ToolCallID]
			d.mu.RUnlock()
			d.emitTurnEvent(active, TurnEvent{Type: "operation_updated", TurnID: active.id, ToolCall: &tool, Operation: &op})
		}
	}
}

func sessionUpdateText(update SessionUpdate) string {
	if update.Text != "" {
		return update.Text
	}
	for _, block := range update.Content {
		if block.Type == "text" {
			return block.Text
		}
	}
	return ""
}

func operationPhase(status string) string {
	switch status {
	case "completed":
		return "completed"
	case "failed":
		return "failed"
	case "pending":
		return "proposed"
	default:
		return "running"
	}
}

func inferOperationTarget(update SessionUpdate) OperationTarget {
	if len(update.Locations) > 0 && update.Locations[0].Path != "" {
		return OperationTarget{Type: "path", Value: update.Locations[0].Path}
	}
	if len(update.RawInput) > 0 {
		var raw map[string]any
		if json.Unmarshal(update.RawInput, &raw) == nil {
			if command, ok := raw["command"].(string); ok {
				return OperationTarget{Type: "command", Value: command}
			}
			if url, ok := raw["url"].(string); ok {
				return OperationTarget{Type: "endpoint", Value: url}
			}
		}
	}
	return OperationTarget{Type: "unknown"}
}

func mapPrompt(prompt RuntimePrompt) []ContentBlock {
	if len(prompt.Parts) > 0 {
		return prompt.Parts
	}
	if prompt.Text != "" {
		return []ContentBlock{{Type: "text", Text: prompt.Text}}
	}
	for _, message := range prompt.Messages {
		if message.Role == "" || message.Role == "user" {
			return message.Parts
		}
	}
	return []ContentBlock{{Type: "text", Text: ""}}
}

func promptText(prompt RuntimePrompt) string {
	if prompt.Text != "" {
		return prompt.Text
	}
	for _, part := range mapPrompt(prompt) {
		if part.Type == "text" {
			return part.Text
		}
	}
	return ""
}

func (d *acpSessionDriver) Snapshot() RuntimeSnapshot {
	d.mu.RLock()
	defer d.mu.RUnlock()
	rawConfig := map[string]any{}
	for key, value := range d.rawConfig {
		rawConfig[key] = value
	}
	return RuntimeSnapshot{
		Version:       RuntimeSnapshotVersion,
		Agent:         d.agent,
		CWD:           d.cwd,
		MCPServers:    append([]MCPServer(nil), d.mcpServers...),
		Session:       RuntimeSnapshotSession{ID: d.sessionID},
		CurrentModeID: d.metadata.CurrentModeID,
		RawConfig:     rawConfig,
	}
}

func (d *acpSessionDriver) Status() string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.status
}

func (d *acpSessionDriver) Capabilities() RuntimeCapabilities {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.capabilities
}

func (d *acpSessionDriver) Diagnostics() RuntimeDiagnostics {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.diagnostics
}

func (d *acpSessionDriver) Metadata() RuntimeSessionMetadata {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.metadata
}

func (d *acpSessionDriver) ThreadEntries() []ThreadEntry {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return append([]ThreadEntry(nil), d.thread...)
}

func (d *acpSessionDriver) ToolCalls() []ToolCallSnapshot {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make([]ToolCallSnapshot, 0, len(d.toolCalls))
	for _, item := range d.toolCalls {
		out = append(out, item)
	}
	return out
}

func (d *acpSessionDriver) Operations() []Operation {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make([]Operation, 0, len(d.operations))
	for _, item := range d.operations {
		out = append(out, item)
	}
	return out
}

func (d *acpSessionDriver) PermissionRequests() []PermissionRequestSnapshot {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make([]PermissionRequestSnapshot, 0, len(d.permissions))
	for _, item := range d.permissions {
		out = append(out, item)
	}
	return out
}
