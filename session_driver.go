package acpruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

type SessionDriver interface {
	Close(context.Context) error
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
}

type activeTurn struct {
	id         string
	events     chan TurnEvent
	completion chan TurnResult
	outputText string
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
}

func newACPSessionDriver(bootstrap sessionBootstrap) *acpSessionDriver {
	driver := &acpSessionDriver{
		connection:   bootstrap.Connection,
		dispose:      bootstrap.Dispose,
		agent:        bootstrap.Agent,
		cwd:          bootstrap.CWD,
		mcpServers:   append([]MCPServer(nil), bootstrap.MCPServers...),
		profile:      bootstrap.Profile,
		sessionID:    bootstrap.SessionResponse.SessionID,
		status:       "ready",
		capabilities: capabilitiesFromInitialize(bootstrap.InitializeResponse),
		metadata:     metadataFromSessionResponse(bootstrap.SessionResponse),
		toolCalls:    map[string]ToolCallSnapshot{},
		operations:   map[string]Operation{},
		permissions:  map[string]PermissionRequestSnapshot{},
		rawConfig:    map[string]any{},
	}
	driver.metadata.SessionID = bootstrap.SessionResponse.SessionID
	bootstrap.Connection.SetSessionUpdateHandler(func(ctx context.Context, notification SessionNotification) {
		driver.handleSessionUpdate(notification)
	})
	return driver
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
	for _, option := range resp.ConfigOptions {
		metadata.AgentConfigOptions = append(metadata.AgentConfigOptions, runtimeConfigOptionFromACP(option))
	}
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

func (d *acpSessionDriver) Close(ctx context.Context) error {
	d.mu.Lock()
	if d.status == "closed" {
		d.mu.Unlock()
		return nil
	}
	d.status = "closed"
	d.mu.Unlock()
	_ = d.connection.CloseSession(ctx, CloseSessionRequest{SessionID: d.sessionID})
	if d.dispose != nil {
		return d.dispose(ctx)
	}
	return nil
}

func (d *acpSessionDriver) CancelTurn(ctx context.Context, turnID string) (bool, error) {
	d.mu.RLock()
	active := d.currentTurn
	d.mu.RUnlock()
	if active == nil || active.id != turnID {
		return false, nil
	}
	if err := d.connection.Cancel(ctx, CancelRequest{SessionID: d.sessionID}); err != nil {
		return false, err
	}
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
	if err := d.connection.SetSessionConfigOption(ctx, SetSessionConfigOptionRequest{SessionID: d.sessionID, OptionID: id, Value: value}); err != nil {
		return err
	}
	d.mu.Lock()
	d.rawConfig[id] = value
	for i := range d.metadata.AgentConfigOptions {
		if d.metadata.AgentConfigOptions[i].ID == id {
			d.metadata.AgentConfigOptions[i].Value = value
		}
	}
	d.mu.Unlock()
	return nil
}

func (d *acpSessionDriver) StartTurn(ctx context.Context, prompt RuntimePrompt) TurnHandle {
	d.mu.Lock()
	d.turnSeq++
	turnID := fmt.Sprintf("turn-%d", d.turnSeq)
	active := &activeTurn{
		id:         turnID,
		events:     make(chan TurnEvent, 64),
		completion: make(chan TurnResult, 1),
	}
	d.currentTurn = active
	d.status = "running"
	d.thread = append(d.thread, ThreadEntry{ID: turnID + "-user", Kind: "user_message", Status: "completed", Text: promptText(prompt), CreatedAt: time.Now(), UpdatedAt: time.Now()})
	d.mu.Unlock()
	active.events <- TurnEvent{Type: "started", TurnID: turnID}
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
	outputText := active.outputText
	d.mu.RUnlock()
	completion := TurnCompletion{TurnID: active.id, OutputText: outputText, StopReason: resp.StopReason, Usage: resp.Usage}
	d.finishTurn(active, completion, nil)
}

func (d *acpSessionDriver) finishTurn(active *activeTurn, completion TurnCompletion, err error) {
	d.mu.Lock()
	if d.currentTurn == active {
		d.currentTurn = nil
		d.status = "ready"
	}
	if err == nil {
		d.thread = append(d.thread, ThreadEntry{ID: active.id + "-assistant", Kind: "assistant_message", Status: "completed", Text: completion.OutputText, CreatedAt: time.Now(), UpdatedAt: time.Now()})
	}
	d.mu.Unlock()
	if err != nil {
		active.events <- TurnEvent{Type: "failed", TurnID: active.id, Error: err}
		active.completion <- TurnResult{Err: err}
	} else {
		active.events <- TurnEvent{Type: "completed", TurnID: active.id, Completion: &completion}
		active.completion <- TurnResult{Completion: completion}
	}
	close(active.events)
	close(active.completion)
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
			active.outputText += update.Text
		}
	case "current_mode_update":
		if update.CurrentModeID != "" {
			d.metadata.CurrentModeID = update.CurrentModeID
		}
	case "config_option_update":
		for _, option := range update.ConfigOptions {
			converted := runtimeConfigOptionFromACP(option)
			replaced := false
			for i := range d.metadata.AgentConfigOptions {
				if d.metadata.AgentConfigOptions[i].ID == converted.ID {
					d.metadata.AgentConfigOptions[i] = converted
					replaced = true
					break
				}
			}
			if !replaced {
				d.metadata.AgentConfigOptions = append(d.metadata.AgentConfigOptions, converted)
			}
		}
	case "session_info_update":
		if update.Title != nil {
			d.metadata.Title = *update.Title
		}
		if update.UpdatedAt != nil {
			d.metadata.UpdatedAt = *update.UpdatedAt
		}
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
			snapshot := ToolCallSnapshot{ID: id, Title: title, Kind: kind, Status: status, Content: update.Content, RawInput: update.RawInput, RawOutput: update.RawOutput, UpdatedAt: time.Now()}
			d.toolCalls[id] = snapshot
			d.operations[id] = Operation{ID: id, Kind: d.profile.MapOperationKind(kind), Phase: operationPhase(status), Title: title, Target: inferOperationTarget(update), UpdatedAt: time.Now()}
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
				snapshot.Content = update.Content
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
		}
	}
	d.mu.Unlock()
	if active != nil {
		switch update.SessionUpdate {
		case "agent_message_chunk":
			active.events <- TurnEvent{Type: "text", TurnID: active.id, Text: update.Text}
		case "agent_thought_chunk":
			active.events <- TurnEvent{Type: "thinking", TurnID: active.id, Thinking: update.Text}
		case "plan":
			active.events <- TurnEvent{Type: "plan_updated", TurnID: active.id, Plan: update.Entries}
		case "usage_update":
			active.events <- TurnEvent{Type: "usage_updated", TurnID: active.id, Usage: update.Usage}
		case "session_info_update":
			active.events <- TurnEvent{Type: "metadata_updated", TurnID: active.id}
		case "tool_call", "tool_call_update":
			if update.ToolCallID != "" {
				d.mu.RLock()
				tool := d.toolCalls[update.ToolCallID]
				op := d.operations[update.ToolCallID]
				d.mu.RUnlock()
				active.events <- TurnEvent{Type: "operation_updated", TurnID: active.id, ToolCall: &tool, Operation: &op}
			}
		}
	}
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
