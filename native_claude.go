package acpruntime

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// claudeNativeEngine drives Claude Code CLI processes, ONE PROCESS PER
// SESSION: each ACP session gets its own headless stream-json process with
// its own spawn flags (model/permission-mode/system prompt/MCP config), so
// sessions on the same connection are fully isolated — including MCP.
//
// The spawn is deferred to the first prompt so InitialConfig (delivered via
// set_mode/set_config_option AFTER session/new) can join the spawn flags.
// claude emits system/init lazily (after the first user message), so the ACP
// session id is synthetic; the real uuid is recorded on the process.
type claudeNativeEngine struct {
	opts  nativeEngineOptions
	mu    sync.Mutex
	procs map[string]*claudeProc // ACP session id -> its own CLI process
}

func (e *claudeNativeEngine) Name() string { return "claude" }

// SessionConfigOptions advertises the spawn-time knobs InitialConfig can set.
func (e *claudeNativeEngine) SessionConfigOptions() []SessionConfigOption {
	modelCategory := "model"
	modeCategory := "mode"
	return []SessionConfigOption{
		{Type: "string", ID: "model", Name: "Model", Category: &modelCategory, Value: ""},
		{Type: "string", ID: "mode", Name: "Permission mode", Category: &modeCategory, Value: ""},
	}
}

// SetSpawnOption records model/permission-mode for ONE session (applied at
// that session's deferred spawn; after spawn it fails loudly — these are
// argv-only options).
func (e *claudeNativeEngine) SetSpawnOption(sessionID string, key string, value any) error {
	proc, err := e.proc(sessionID)
	if err != nil {
		return err
	}
	return proc.setSpawnOption(key, value)
}

func (e *claudeNativeEngine) Start(ctx context.Context, opts nativeEngineOptions) error {
	e.opts = opts
	if _, err := exec.LookPath(opts.Agent.Command); err != nil {
		return wrapError(ErrorProcess, "native.claude.spawn", "claude binary not found on PATH", err)
	}
	e.mu.Lock()
	e.procs = map[string]*claudeProc{}
	e.mu.Unlock()
	return nil
}

func (e *claudeNativeEngine) proc(sessionID string) (*claudeProc, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	p, ok := e.procs[sessionID]
	if !ok {
		return nil, &RuntimeError{Kind: ErrorProcess, Op: "native.claude.proc", Msg: "no session " + sessionID + " on this connection"}
	}
	return p, nil
}

func (e *claudeNativeEngine) NewSession(ctx context.Context, opts nativeEngineOptions, req NewSessionRequest) (string, error) {
	proc := &claudeProc{
		eng:        e,
		acpSessionID: fmt.Sprintf("claude-%d", time.Now().UnixNano()),
		pendingReq: &req,
	}
	e.mu.Lock()
	e.procs[proc.acpSessionID] = proc
	e.mu.Unlock()
	return proc.acpSessionID, nil
}

// LoadSession re-attaches to a PREVIOUS claude conversation by respawning the
// CLI with --resume <session id>. Spawns immediately: the requested session
// id is known and no deferred options are expected.
func (e *claudeNativeEngine) LoadSession(ctx context.Context, opts nativeEngineOptions, req LoadSessionRequest) (string, error) {
	proc := &claudeProc{
		eng:          e,
		acpSessionID: req.SessionID,
		resumeFrom:   req.SessionID,
		pendingReq:   &NewSessionRequest{MCPServers: req.MCPServers},
	}
	e.mu.Lock()
	e.procs[req.SessionID] = proc
	e.mu.Unlock()
	if err := proc.ensureSpawned(ctx); err != nil {
		e.mu.Lock()
		delete(e.procs, req.SessionID)
		e.mu.Unlock()
		return "", err
	}
	return req.SessionID, nil
}

// ForkSession derives a NEW session lineage from a previous conversation:
// spawn carries --resume <id> --fork-session, so the original stays intact.
// claude mints the derived uuid lazily, so the ACP id here is synthetic.
func (e *claudeNativeEngine) ForkSession(ctx context.Context, opts nativeEngineOptions, req ForkSessionRequest) (string, error) {
	proc := &claudeProc{
		eng:          e,
		acpSessionID: fmt.Sprintf("claude-%d", time.Now().UnixNano()),
		resumeFrom:   req.SessionID,
		forkSession:  true,
		pendingReq:   &NewSessionRequest{MCPServers: req.MCPServers},
	}
	e.mu.Lock()
	e.procs[proc.acpSessionID] = proc
	e.mu.Unlock()
	return proc.acpSessionID, nil
}

func (e *claudeNativeEngine) Prompt(ctx context.Context, opts nativeEngineOptions, sessionID string, blocks []ContentBlock) (nativeTurnResult, error) {
	proc, err := e.proc(sessionID)
	if err != nil {
		return nativeTurnResult{}, err
	}
	return proc.prompt(ctx, blocks)
}

func (e *claudeNativeEngine) Cancel(ctx context.Context, opts nativeEngineOptions, sessionID string) {
	proc, err := e.proc(sessionID)
	if err != nil {
		return
	}
	proc.cancel()
}

func (e *claudeNativeEngine) Close(ctx context.Context) error {
	e.mu.Lock()
	procs := make([]*claudeProc, 0, len(e.procs))
	for _, p := range e.procs {
		procs = append(procs, p)
	}
	e.procs = map[string]*claudeProc{}
	e.mu.Unlock()
	for _, p := range procs {
		_ = p.kill()
	}
	return nil
}

// claudeProc is one dedicated headless claude process for one session.
type claudeProc struct {
	eng *claudeNativeEngine

	mu             sync.Mutex
	cmd            *exec.Cmd
	stdin          io.WriteCloser
	loopOn         context.CancelFunc
	realSessionID  string // claude's own session uuid (init event)
	acpSessionID   string // synthetic ACP session id
	model          string // spawn-time --model
	permissionMode string // spawn-time --permission-mode
	resumeFrom     string // --resume source (load/fork)
	forkSession    bool   // pair resumeFrom with --fork-session
	pendingReq     *NewSessionRequest
	spawned        bool
	initErr        error
	turn           *claudeTurn
	planTools      map[string]bool // tool ids that carried TodoWrite plans
	mcpFile        string
	settingsFile   string
}

type claudeTurn struct {
	done     chan struct{}
	stop     string
	usage    *Usage
	err      error
	finished bool
}

func (p *claudeProc) setSpawnOption(key string, value any) error {
	s, ok := value.(string)
	if !ok {
		return &RuntimeError{Kind: ErrorProtocol, Op: "native.claude.config", Msg: fmt.Sprintf("option %q expects a string value", key)}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.spawned {
		return &RuntimeError{Kind: ErrorProtocol, Op: "native.claude.config", Msg: fmt.Sprintf("option %q is a spawn-time setting and the engine has already started", key)}
	}
	switch key {
	case "model":
		p.model = s
	case "mode", "permissionMode":
		p.permissionMode = s
	default:
		return &RuntimeError{Kind: ErrorProtocol, Op: "native.claude.config", Msg: fmt.Sprintf("option %q is not supported by the native claude transport", key)}
	}
	return nil
}

func (p *claudeProc) alive() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.spawned && p.cmd != nil && p.cmd.Process != nil
}

// buildArgs translates the pending request and recorded spawn options into
// claude spawn flags.
func (p *claudeProc) buildArgs() ([]string, error) {
	args := []string{"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose"}
	var meta map[string]any
	var servers []MCPServer
	if p.pendingReq != nil {
		meta = p.pendingReq.Meta
		servers = p.pendingReq.MCPServers
	}
	model := p.model
	if model == "" {
		if v, ok := meta["model"].(string); ok {
			model = v
		}
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	permissionMode := p.permissionMode
	if permissionMode == "" {
		if v, ok := meta["mode"].(string); ok {
			permissionMode = v
		}
	}
	if permissionMode != "" {
		args = append(args, "--permission-mode", permissionMode)
	}
	if p.resumeFrom != "" {
		args = append(args, "--resume", p.resumeFrom)
		if p.forkSession {
			args = append(args, "--fork-session")
		}
	}
	if v, ok := meta[SystemPromptMetaKey].(string); ok && v != "" {
		args = append(args, "--system-prompt", v)
	}
	if v, ok := meta[AppendSystemPromptMetaKey].(string); ok && v != "" {
		args = append(args, "--append-system-prompt", v)
	}
	if cc, ok := meta["claudeCode"].(map[string]any); ok {
		if options, ok := cc["options"].(map[string]any); ok {
			if v := stringSliceFromAny(options["allowedTools"]); len(v) > 0 {
				args = append(args, "--allowedTools", strings.Join(v, ","))
			}
			if v := stringSliceFromAny(options["disallowedTools"]); len(v) > 0 {
				args = append(args, "--disallowedTools", strings.Join(v, ","))
			}
			if settings, ok := options["settings"].(map[string]any); ok && len(settings) > 0 {
				file, err := p.writeJSONTemp("acp-claude-settings", settings)
				if err != nil {
					return nil, err
				}
				args = append(args, "--settings", file)
			}
		}
	}
	if len(servers) > 0 {
		file, err := p.writeMCPConfig(servers)
		if err != nil {
			return nil, err
		}
		args = append(args, "--mcp-config", file, "--strict-mcp-config")
	}
	return args, nil
}

// writeJSONTemp marshals v to a temp file (used for --settings and
// --mcp-config payloads; claude reads both as JSON documents).
func (p *claudeProc) writeJSONTemp(prefix string, v any) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", wrapError(ErrorProcess, "native.claude.config", "marshal "+prefix, err)
	}
	file := filepath.Join(os.TempDir(), fmt.Sprintf("%s-%d.json", prefix, time.Now().UnixNano()))
	if err := os.WriteFile(file, data, 0o600); err != nil {
		return "", wrapError(ErrorProcess, "native.claude.config", "write "+prefix, err)
	}
	p.mu.Lock()
	if strings.Contains(prefix, "mcp") {
		p.mcpFile = file
	} else if strings.Contains(prefix, "settings") {
		p.settingsFile = file
	}
	p.mu.Unlock()
	return file, nil
}

// writeMCPConfig materializes MCP servers as a claude --mcp-config document.
// stdio servers map to command/args/env; HTTP servers map to type/url/headers.
func (p *claudeProc) writeMCPConfig(servers []MCPServer) (string, error) {
	config := map[string]any{"mcpServers": map[string]any{}}
	named := config["mcpServers"].(map[string]any)
	for _, server := range servers {
		switch {
		case server.Command != "":
			entry := map[string]any{"command": server.Command}
			if len(server.Args) > 0 {
				entry["args"] = server.Args
			}
			if len(server.Env) > 0 {
				env := map[string]string{}
				for _, item := range server.Env {
					env[item.Name] = item.Value
				}
				entry["env"] = env
			}
			if server.CWD != "" {
				entry["cwd"] = server.CWD
			}
			named[server.Name] = entry
		case server.URL != "":
			entry := map[string]any{"type": "http", "url": server.URL}
			if len(server.Headers) > 0 {
				headers := map[string]string{}
				for _, header := range server.Headers {
					headers[header.Name] = header.Value
				}
				entry["headers"] = headers
			}
			named[server.Name] = entry
		}
	}
	if len(named) == 0 {
		return "", &RuntimeError{Kind: ErrorProcess, Op: "native.claude.mcp", Msg: "no supported MCP servers (stdio command or http url) in request"}
	}
	return p.writeJSONTemp("acp-claude-mcp", config)
}

// ensureSpawned launches the CLI exactly once, with all spawn-time options.
func (p *claudeProc) ensureSpawned(ctx context.Context) error {
	p.mu.Lock()
	if p.spawned {
		p.mu.Unlock()
		return nil
	}
	opts := p.eng.opts
	p.mu.Unlock()
	args, err := p.buildArgs()
	if err != nil {
		return err
	}
	if err := p.spawn(ctx, opts, args); err != nil {
		return err
	}
	p.mu.Lock()
	p.spawned = true
	p.mu.Unlock()
	return nil
}

// spawn launches the claude CLI with the given args and wires the read loop.
func (p *claudeProc) spawn(ctx context.Context, opts nativeEngineOptions, args []string) error {
	cmd := exec.Command(opts.Agent.Command, append(opts.Agent.Args, args...)...)
	configureProcessGroup(cmd)
	cmd.Dir = opts.CWD
	cmd.Env = envSlice(opts.Agent.Env)
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return wrapError(ErrorProcess, "native.claude.spawn", "stdin pipe", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return wrapError(ErrorProcess, "native.claude.spawn", "stdout pipe", err)
	}
	if err := cmd.Start(); err != nil {
		return wrapError(ErrorProcess, "native.claude.spawn", "failed to spawn claude", err)
	}
	p.mu.Lock()
	p.cmd, p.stdin = cmd, stdin
	p.mu.Unlock()

	loopDone := make(chan struct{})
	var closeLoopOnce sync.Once
	cancelLoop := func() { closeLoopOnce.Do(func() { close(loopDone) }) }
	p.loopOn = cancelLoop
	go func() {
		defer cancelLoop()
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 64*1024), maxRPCMessageSize)
		for scanner.Scan() {
			p.handleLine(scanner.Bytes())
		}
	}()
	return nil
}

func (p *claudeProc) prompt(ctx context.Context, blocks []ContentBlock) (nativeTurnResult, error) {
	if err := p.ensureSpawned(ctx); err != nil {
		return nativeTurnResult{}, err
	}
	p.mu.Lock()
	if p.turn != nil && !p.turn.finished {
		p.mu.Unlock()
		return nativeTurnResult{}, &RuntimeError{Kind: ErrorProcess, Op: "native.claude.turn", Msg: "a turn is already in flight"}
	}
	turn := &claudeTurn{done: make(chan struct{}), stop: "end_turn"}
	p.turn = turn
	p.mu.Unlock()

	msg, _ := json.Marshal(map[string]any{
		"type":               "user",
		"message":            map[string]any{"role": "user", "content": claudeContentFromBlocks(blocks)},
		"parent_tool_use_id": nil,
	})
	p.writeLine(msg)

	select {
	case <-turn.done:
	case <-ctx.Done():
		return nativeTurnResult{}, ctx.Err()
	}
	if turn.err != nil {
		return nativeTurnResult{}, &RuntimeError{Kind: ErrorProcess, Op: "native.claude.turn", Msg: turn.err.Error()}
	}
	return nativeTurnResult{StopReason: turn.stop, Usage: turn.usage}, nil
}

// cancel sends claude's native interrupt control request. The turn still
// settles via its result event.
func (p *claudeProc) cancel() {
	p.mu.Lock()
	turn := p.turn
	p.mu.Unlock()
	if turn == nil || turn.finished {
		return
	}
	data, _ := json.Marshal(map[string]any{
		"type":       "control_request",
		"request_id": time.Now().UnixNano(),
		"request":    map[string]any{"subtype": "interrupt"},
	})
	p.writeLine(data)
}

// handleLine processes one claude stream-json event.
func (p *claudeProc) handleLine(line []byte) {
	var ev struct {
		Type       string          `json:"type"`
		Subtype    string          `json:"subtype"`
		SessionID  string          `json:"session_id"`
		IsError    bool            `json:"is_error"`
		Result     string          `json:"result"`
		StopReason string          `json:"stop_reason"`
		Usage      json.RawMessage `json:"usage"`
		SlashCommands  []string        `json:"slash_commands"`
		PermissionMode string          `json:"permissionMode"`
		Message    *struct {
			ID      string          `json:"id"`
			Content json.RawMessage `json:"content"`
		} `json:"message"`
		RequestID any `json:"request_id"`
		Request   *struct {
			Subtype  string          `json:"subtype"`
			ToolName string          `json:"tool_name"`
			Input    json.RawMessage `json:"input"`
		} `json:"request"`
	}
	if err := json.Unmarshal(line, &ev); err != nil {
		return
	}
	switch ev.Type {
	case "system":
		if ev.Subtype == "init" {
			if ev.SessionID != "" {
				p.mu.Lock()
				p.realSessionID = ev.SessionID
				p.mu.Unlock()
			}
			if len(ev.SlashCommands) > 0 {
				commands := make([]AvailableCommand, 0, len(ev.SlashCommands))
				for _, name := range ev.SlashCommands {
					commands = append(commands, AvailableCommand{Name: strings.TrimPrefix(name, "/")})
				}
				p.emit(SessionUpdate{
					SessionUpdate:     "available_commands_update",
					AvailableCommands: commands,
				})
			}
			if ev.PermissionMode != "" {
				p.emit(SessionUpdate{SessionUpdate: "current_mode_update", CurrentModeID: ev.PermissionMode})
			}
		}
	case "assistant":
		if ev.Message == nil {
			return
		}
		p.handleAssistantBlocks(ev.Message.ID, ev.Message.Content)
	case "user":
		// tool_result blocks ride on user messages in this protocol.
		if ev.Message == nil {
			return
		}
		var blocks []claudeBlock
		if err := json.Unmarshal(ev.Message.Content, &blocks); err != nil {
			return
		}
		for _, block := range blocks {
			if block.Type != "tool_result" || block.ToolUseID == "" {
				continue
			}
			p.mu.Lock()
			isPlan := p.planTools != nil && p.planTools[block.ToolUseID]
			p.mu.Unlock()
			if isPlan {
				continue
			}
			completed := "completed"
			p.emit(SessionUpdate{
				SessionUpdate: "tool_call_update",
				ToolCallID:    block.ToolUseID,
				Status:        &completed,
			})
		}
	case "result":
		p.mu.Lock()
		turn := p.turn
		if turn != nil && !turn.finished {
			turn.finished = true
			turn.stop = ev.StopReason
			if turn.stop == "" {
				turn.stop = "end_turn"
			}
			if ev.IsError {
				msg := ev.Result
				if msg == "" {
					msg = "claude turn failed"
				}
				turn.err = fmt.Errorf("%s", msg)
			}
			turn.usage = claudeUsageFromRaw(ev.Usage)
			close(turn.done)
		}
		p.mu.Unlock()
	case "control_request":
		if ev.Request != nil && ev.Request.Subtype == "can_use_tool" {
			p.handleCanUseTool(ev.RequestID, ev.Request)
		}
	}
}

// handleCanUseTool maps claude's native permission prompt onto the ACP
// session/request_permission round-trip with the host authority.
func (p *claudeProc) handleCanUseTool(requestID any, req *struct {
	Subtype  string          `json:"subtype"`
	ToolName string          `json:"tool_name"`
	Input    json.RawMessage `json:"input"`
}) {
	if p.eng.opts.requestPermission == nil {
		p.writeControlResponse(requestID, "deny", "no permission authority configured")
		return
	}
	permissionReq := PermissionRequest{
		SessionID:  p.acpSessionIDLocked(),
		ToolCallID: fmt.Sprintf("can_use_tool-%v", requestID),
		Title:      req.ToolName,
		Kind:       "execute",
		Options: []PermissionOption{
			{ID: "allow", Name: "Allow", Kind: "allow_once"},
			{ID: "deny", Name: "Deny", Kind: "reject_once"},
		},
	}
	decision, err := p.eng.opts.requestPermission(context.Background(), permissionReq)
	if err != nil {
		p.writeControlResponse(requestID, "deny", err.Error())
		return
	}
	if decision.Outcome == "selected" && decision.OptionID == "allow" {
		updated := map[string]any{}
		_ = json.Unmarshal(req.Input, &updated)
		p.writeControlResponseWithInput(requestID, "allow", "", updated)
		return
	}
	p.writeControlResponse(requestID, "deny", "denied by host authority")
}

func (p *claudeProc) writeControlResponse(requestID any, behavior, message string) {
	p.writeControlResponseWithInput(requestID, behavior, message, nil)
}

func (p *claudeProc) writeControlResponseWithInput(requestID any, behavior, message string, updatedInput map[string]any) {
	inner := map[string]any{"behavior": behavior}
	if behavior == "allow" && updatedInput != nil {
		inner["updatedInput"] = updatedInput
	}
	if message != "" {
		inner["message"] = message
	}
	response := map[string]any{
		"type":     "control_response",
		"response": map[string]any{"subtype": "success", "request_id": requestID, "response": inner},
	}
	data, err := json.Marshal(response)
	if err != nil {
		return
	}
	p.writeLine(data)
}

func (p *claudeProc) handleAssistantBlocks(messageID string, raw json.RawMessage) {
	var blocks []claudeBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return
	}
	for _, block := range blocks {
		switch block.Type {
		case "text":
			if block.Text != "" {
				p.emit(SessionUpdate{SessionUpdate: "agent_message_chunk", MessageID: messageID, Text: block.Text})
			}
		case "thinking":
			if block.Thinking != "" {
				p.emit(SessionUpdate{SessionUpdate: "agent_thought_chunk", Text: block.Thinking})
			}
		case "tool_use":
			if block.ID == "" {
				continue
			}
			if block.Name == "TodoWrite" {
				// claude-acp projects the todo list into ACP plan entries; do
				// the same so read models agree across transports.
				p.emitPlanFromTodoWrite(block.ID, block.Input)
				continue
			}
			title := block.Name
			pending := "pending"
			p.emit(SessionUpdate{
				SessionUpdate: "tool_call",
				ToolCallID:    block.ID,
				Title:         &title,
				Kind:          strPtr(claudeToolKind(block.Name)),
				Status:        &pending,
			})
		}
	}
}

// emitPlanFromTodoWrite converts a TodoWrite tool_use input into an ACP plan
// update; the tool id is remembered so the later tool_result does not create
// a phantom tool call.
func (p *claudeProc) emitPlanFromTodoWrite(toolUseID string, input json.RawMessage) {
	var payload struct {
		Todos []struct {
			Content  string `json:"content"`
			Status   string `json:"status"`
			Priority string `json:"priority"`
		} `json:"todos"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return
	}
	p.mu.Lock()
	if p.planTools == nil {
		p.planTools = map[string]bool{}
	}
	p.planTools[toolUseID] = true
	p.mu.Unlock()
	entries := make([]PlanEntry, 0, len(payload.Todos))
	for _, todo := range payload.Todos {
		entries = append(entries, PlanEntry{Content: todo.Content, Status: todo.Status, Priority: todo.Priority})
	}
	p.emit(SessionUpdate{SessionUpdate: "plan", Entries: entries})
}

func (p *claudeProc) emit(update SessionUpdate) {
	p.mu.Lock()
	sessionID := p.acpSessionID
	p.mu.Unlock()
	p.eng.opts.emit(sessionID, update)
}

func (p *claudeProc) acpSessionIDLocked() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.acpSessionID
}

// kill terminates this session's process tree and temp files.
func (p *claudeProc) kill() error {
	p.mu.Lock()
	cmd, stdin, loopOn := p.cmd, p.stdin, p.loopOn
	mcpFile, settingsFile := p.mcpFile, p.settingsFile
	p.mu.Unlock()
	if loopOn != nil {
		defer loopOn()
	}
	if stdin != nil {
		_ = stdin.Close()
	}
	if mcpFile != "" {
		_ = os.Remove(mcpFile)
	}
	if settingsFile != "" {
		_ = os.Remove(settingsFile)
	}
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	pgid := processGroupIDAfterStart(cmd)
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	select {
	case <-waitCh:
		return nil
	case <-time.After(1500 * time.Millisecond):
		_ = signalProcessTree(pgid, cmd.Process, syscall.SIGTERM)
		select {
		case <-waitCh:
			return nil
		case <-time.After(time.Second):
			_ = signalProcessTree(pgid, cmd.Process, syscall.SIGKILL)
			select {
			case <-waitCh:
			case <-time.After(3 * time.Second):
			}
			return nil
		}
	}
}

// claudeContentFromBlocks maps ACP prompt blocks onto anthropic message
// content (text, base64/url images, resource links as text). Audio blocks are
// skipped: the engine accepts no audio input.
func claudeContentFromBlocks(blocks []ContentBlock) []map[string]any {
	content := make([]map[string]any, 0, len(blocks))
	appendText := func(text string) {
		content = append(content, map[string]any{"type": "text", "text": text})
	}
	for _, block := range blocks {
		switch block.Type {
		case "text":
			if block.Text != "" {
				appendText(block.Text)
			}
		case "image":
			switch {
			case block.Data != "":
				mime := block.MimeType
				if mime == "" {
					mime = "image/png"
				}
				content = append(content, map[string]any{"type": "image", "source": map[string]any{
					"type": "base64", "media_type": mime, "data": block.Data}})
			case block.URI != "":
				content = append(content, map[string]any{"type": "image", "source": map[string]any{
					"type": "url", "url": block.URI}})
			}
		case "resource_link":
			appendText(strings.TrimSpace(block.Name + ": " + block.URI))
		case "resource":
			var res struct {
				Text string `json:"text"`
				URI  string `json:"uri"`
				Name string `json:"name"`
			}
			if err := json.Unmarshal(block.Resource, &res); err == nil {
				switch {
				case res.Text != "":
					appendText(res.Text)
				case res.URI != "":
					appendText(strings.TrimSpace(res.Name + ": " + res.URI))
				}
			}
		case "audio":
			// claude accepts no audio input; skipped deliberately.
		}
	}
	return content
}

// claudeBlock is the subset of anthropic content blocks this adapter maps.
type claudeBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
}

// claudeToolKind maps claude tool names onto ACP tool kinds (best-effort).
func claudeToolKind(name string) string {
	switch name {
	case "Bash", "Task":
		return "execute_command"
	case "Read":
		return "read_file"
	case "Write", "Edit", "NotebookEdit":
		return "write_file"
	case "WebFetch":
		return "network_request"
	case "WebSearch", "Grep", "Glob":
		return "search"
	default:
		return "other"
	}
}

func claudeUsageFromRaw(raw json.RawMessage) *Usage {
	if len(raw) == 0 {
		return nil
	}
	var u struct {
		InputTokens              uint64 `json:"input_tokens"`
		CacheCreationInputTokens uint64 `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     uint64 `json:"cache_read_input_tokens"`
		OutputTokens             uint64 `json:"output_tokens"`
	}
	if err := json.Unmarshal(raw, &u); err != nil {
		return nil
	}
	cachedRead := u.CacheReadInputTokens
	cacheWrite := u.CacheCreationInputTokens
	total := u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens + u.OutputTokens
	return &Usage{
		TotalTokens:       total,
		InputTokens:       u.InputTokens,
		OutputTokens:      u.OutputTokens,
		CachedReadTokens:  &cachedRead,
		CachedWriteTokens: &cacheWrite,
	}
}

func stringSliceFromAny(v any) []string {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func (p *claudeProc) writeLine(data []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stdin == nil {
		return
	}
	_, _ = p.stdin.Write(append(data, '\n'))
}
