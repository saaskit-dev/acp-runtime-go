package acpruntime

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// claudeNativeEngine drives Claude Code CLI processes, ONE PROCESS PER
// SESSION: each ACP session gets its own headless stream-json process with
// its own spawn flags (model/permission-mode/system prompt/MCP config), so
// sessions on the same connection are fully isolated — including MCP.
//
// New-session spawning is deferred to the first prompt; session configuration
// is resolved before create/resume, and resume spawns with those resolved flags.
// Claude emits system/init lazily, so the runtime allocates a resumable UUID and
// supplies it with --session-id instead of exposing an unrelated synthetic ID.
type claudeNativeEngine struct {
	opts   nativeEngineOptions
	mu     sync.Mutex
	procs  map[string]*claudeProc // ACP session id -> its owned CLI process
	closed bool
}

func (e *claudeNativeEngine) Name() string { return "claude" }

// SessionConfigOptions advertises the spawn-time knobs InitialConfig can set.
func (e *claudeNativeEngine) SessionConfigOptions() []SessionConfigOption {
	modelCategory := "model"
	modeCategory := "mode"
	return []SessionConfigOption{
		{Type: "select", ID: "model", Name: "Model", Category: &modelCategory, Value: "", Options: []SessionConfigChoice{{Value: "", Name: "Provider default"}}},
		{Type: "select", ID: "mode", Name: "Permission mode", Category: &modeCategory, Value: "default", Options: claudeModeChoices()},
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
	e.closed = false
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

func newClaudeNativeSessionID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:]), nil
}

func (e *claudeNativeEngine) NewSession(ctx context.Context, opts nativeEngineOptions, req NewSessionRequest) (string, error) {
	if err := validateClaudeNativeMeta(req.Meta); err != nil {
		return "", err
	}
	id, err := newClaudeNativeSessionID()
	if err != nil {
		return "", wrapError(ErrorCreate, "native.claude.session_id", "create session identity", err)
	}
	proc := &claudeProc{
		eng:          e,
		acpSessionID: id,
		pendingReq:   &req,
	}
	e.mu.Lock()
	e.procs[proc.acpSessionID] = proc
	e.mu.Unlock()
	return proc.acpSessionID, nil
}

// LoadSession re-attaches to a PREVIOUS claude conversation by respawning the
// CLI with --resume <session id>. Spawns immediately: the requested session
// id and resolved startup options are already known.
func (e *claudeNativeEngine) LoadSession(ctx context.Context, opts nativeEngineOptions, req LoadSessionRequest) (string, error) {
	if err := validateClaudeNativeMeta(req.Meta); err != nil {
		return "", err
	}
	proc := &claudeProc{
		eng:          e,
		acpSessionID: req.SessionID,
		resumeFrom:   req.SessionID,
		pendingReq:   &NewSessionRequest{Meta: req.Meta, MCPServers: req.MCPServers, AdditionalDirectories: req.AdditionalDirectories},
	}
	e.mu.Lock()
	e.procs[req.SessionID] = proc
	e.mu.Unlock()
	if err := proc.ensureSpawned(ctx); err != nil {
		cleanupErr := proc.kill(context.Background())
		if cleanupErr == nil {
			e.mu.Lock()
			if e.procs[req.SessionID] == proc {
				delete(e.procs, req.SessionID)
			}
			e.mu.Unlock()
		}
		return "", errors.Join(err, cleanupErr)
	}
	return req.SessionID, nil
}

// ForkSession derives a NEW session lineage from a previous conversation:
// spawn carries --resume <id> --fork-session, so the original stays intact.
// claude mints the derived uuid lazily, so the ACP id here is synthetic.
func (e *claudeNativeEngine) ForkSession(ctx context.Context, opts nativeEngineOptions, req ForkSessionRequest) (string, error) {
	id, err := newClaudeNativeSessionID()
	if err != nil {
		return "", wrapError(ErrorFork, "native.claude.session_id", "create fork identity", err)
	}
	proc := &claudeProc{
		eng:          e,
		acpSessionID: id,
		resumeFrom:   req.SessionID,
		forkSession:  true,
		pendingReq:   &NewSessionRequest{Meta: req.Meta, MCPServers: req.MCPServers, AdditionalDirectories: req.AdditionalDirectories},
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
	e.closed = true
	procs := make(map[string]*claudeProc, len(e.procs))
	for id, proc := range e.procs {
		procs[id] = proc
	}
	e.mu.Unlock()
	var cleanupErrors []error
	for id, proc := range procs {
		if err := proc.kill(ctx); err != nil {
			cleanupErrors = append(cleanupErrors, err)
			continue
		}
		e.mu.Lock()
		delete(e.procs, id)
		e.mu.Unlock()
	}
	return errors.Join(cleanupErrors...)
}

// claudeProc is one dedicated headless claude process for one session.
type claudeProc struct {
	eng *claudeNativeEngine

	mu             sync.Mutex
	cmd            *exec.Cmd
	stdin          io.WriteCloser
	loopOn         context.CancelFunc
	exitDone       <-chan struct{}
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
	writeMu        sync.Mutex
	writeQueue     chan []byte
	writerDone     chan struct{}
	approvals      map[string]*claudeApproval
	approvalSlots  chan struct{}
	wait           nativeProcessWait
}

type claudeTurn struct {
	done             chan struct{}
	stop             string
	usage            *Usage
	err              error
	finished         bool
	permissionCtx    context.Context
	cancelPermission context.CancelFunc
	cancelling       bool
}

func (p *claudeProc) setSpawnOption(key string, value any) error {
	s, ok := value.(string)
	if !ok {
		return &RuntimeError{Kind: ErrorProtocol, Op: "native.claude.config", Msg: fmt.Sprintf("option %q expects a string value", key)}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.spawned {
		if (key == "model" && s == p.model) || ((key == "mode" || key == "permissionMode") && s == p.permissionMode) {
			return nil
		}
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
		for _, directory := range p.pendingReq.AdditionalDirectories {
			args = append(args, "--add-dir", directory)
		}
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
	p.mu.Lock()
	p.model = model
	p.mu.Unlock()
	permissionMode := p.permissionMode
	if permissionMode == "" {
		if v, ok := meta["mode"].(string); ok {
			permissionMode = v
		}
	}
	if permissionMode != "" {
		args = append(args, "--permission-mode", permissionMode)
		if permissionMode == "bypassPermissions" {
			args = append(args, "--allow-dangerously-skip-permissions")
		}
	}
	p.mu.Lock()
	p.permissionMode = permissionMode
	p.mu.Unlock()
	if p.resumeFrom == "" || p.forkSession {
		args = append(args, "--session-id", p.acpSessionID)
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
			if tools, present := options["tools"]; present && tools != nil {
				args = append(args, "--tools", strings.Join(stringSliceFromAny(tools), ","))
			}
			if sources, present := options["settingSources"]; present && sources != nil {
				args = append(args, "--setting-sources", strings.Join(stringSliceFromAny(sources), ","))
			}
			if plugins, ok := options["plugins"].([]any); ok {
				for _, entry := range plugins {
					plugin, _ := entry.(map[string]any)
					if plugin["type"] == "local" {
						if directory, ok := plugin["path"].(string); ok && directory != "" {
							args = append(args, "--plugin-dir", directory)
						}
					}
				}
			}
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
	p.eng.mu.Lock()
	defer p.eng.mu.Unlock()
	if p.eng.closed {
		return wrapError(ErrorSessionClosed, "native.claude.spawn", "native connection is closed", nil)
	}
	p.mu.Lock()
	if p.spawned {
		err := p.initErr
		p.mu.Unlock()
		return err
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
	p.exitDone = loopDone
	p.writeQueue = make(chan []byte, 64)
	p.writerDone = make(chan struct{})
	go func() {
		defer close(p.writerDone)
		for {
			select {
			case <-loopDone:
				return
			case data := <-p.writeQueue:
				if _, err := stdin.Write(append(data, '\n')); err != nil {
					p.failWrite(err)
					return
				}
			}
		}
	}()
	go func() {
		defer cancelLoop()
		defer p.cancelPermissions()
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
	permissionCtx, cancelPermission := context.WithCancel(context.Background())
	turn := &claudeTurn{done: make(chan struct{}), stop: "end_turn", permissionCtx: permissionCtx, cancelPermission: cancelPermission}
	p.turn = turn
	p.mu.Unlock()
	stopCancel := context.AfterFunc(ctx, p.cancel)
	defer stopCancel()

	msg, _ := json.Marshal(map[string]any{
		"type":               "user",
		"message":            map[string]any{"role": "user", "content": claudeContentFromBlocks(blocks)},
		"parent_tool_use_id": nil,
	})
	if err := p.writeLine(msg); err != nil {
		p.failWrite(err)
		return nativeTurnResult{}, err
	}

	select {
	case <-turn.done:
	case <-p.exitDone:
		select {
		case <-turn.done:
		default:
			return nativeTurnResult{}, wrapError(ErrorProcess, "native.claude.turn", "Claude process stream closed before a terminal result", io.EOF)
		}
	case <-ctx.Done():
		return nativeTurnResult{}, ctx.Err()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if turn.err != nil {
		return nativeTurnResult{}, turn.err
	}
	return nativeTurnResult{StopReason: turn.stop, Usage: turn.usage}, nil
}

// cancel sends claude's native interrupt control request. The turn still
// settles via its result event.
func (p *claudeProc) cancel() {
	p.mu.Lock()
	turn := p.turn
	if turn == nil || turn.finished || turn.cancelling {
		p.mu.Unlock()
		return
	}
	turn.cancelling = true
	if turn.cancelPermission != nil {
		turn.cancelPermission()
	}
	p.mu.Unlock()
	data, _ := json.Marshal(map[string]any{
		"type":       "control_request",
		"request_id": time.Now().UnixNano(),
		"request":    map[string]any{"subtype": "interrupt"},
	})
	if err := p.writeLine(data); err != nil {
		p.failWrite(err)
	}
}

// handleLine processes one claude stream-json event.
func (p *claudeProc) handleLine(line []byte) {
	var ev struct {
		Type           string          `json:"type"`
		Subtype        string          `json:"subtype"`
		SessionID      string          `json:"session_id"`
		IsError        bool            `json:"is_error"`
		Result         string          `json:"result"`
		StopReason     string          `json:"stop_reason"`
		Usage          json.RawMessage `json:"usage"`
		SlashCommands  []string        `json:"slash_commands"`
		PermissionMode string          `json:"permissionMode"`
		Message        *struct {
			ID      string          `json:"id"`
			Content json.RawMessage `json:"content"`
		} `json:"message"`
		RequestID any                      `json:"request_id"`
		Request   *claudePermissionRequest `json:"request"`
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
			if turn.cancelPermission != nil {
				turn.cancelPermission()
			}
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
			p.queueCanUseTool(ev.RequestID, ev.Request, append(json.RawMessage(nil), line...))
		}
	}
}

// claudePermissionRequest maps Claude's native permission prompt onto the ACP
// session/request_permission round-trip with the host authority.
type claudeApproval struct {
	turn    *claudeTurn
	revoked bool
}

type claudePermissionRequest struct {
	Subtype   string          `json:"subtype"`
	ToolName  string          `json:"tool_name"`
	ToolUseID string          `json:"tool_use_id"`
	Input     json.RawMessage `json:"input"`
}

func (p *claudeProc) cancelPermissions() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.turn != nil && p.turn.cancelPermission != nil {
		p.turn.cancelPermission()
	}
}

// Keep native decoding live while approval is outstanding. Both retained input
// and handler concurrency are bounded; duplicate IDs are denied and revoke the
// original request so its delayed allow cannot be applied.
func (p *claudeProc) queueCanUseTool(requestID any, req *claudePermissionRequest, raw json.RawMessage) {
	keyBytes, _ := json.Marshal(requestID)
	key := string(keyBytes)
	p.mu.Lock()
	turn := p.turn
	if p.approvalSlots == nil {
		p.approvalSlots = make(chan struct{}, 8)
	}
	if p.approvals == nil {
		p.approvals = map[string]*claudeApproval{}
	}
	prior, duplicate := p.approvals[key]
	token := &claudeApproval{turn: turn}
	valid := turn != nil && !turn.finished && !turn.cancelling && turn.permissionCtx != nil && !duplicate && len(raw) <= 64*1024 && requestID != nil
	if duplicate {
		prior.revoked = true
	}
	if valid {
		select {
		case p.approvalSlots <- struct{}{}:
			p.approvals[key] = token
		default:
			valid = false
		}
	}
	p.mu.Unlock()
	if !valid {
		p.writeControlResponse(requestID, "deny", "inactive, duplicate, or excessive permission request")
		return
	}
	go func() {
		defer func() { <-p.approvalSlots }()
		ctx, cancel := context.WithTimeout(turn.permissionCtx, 2*time.Minute)
		defer cancel()
		decision := PermissionDecision{}
		var permissionErr error
		input, inputErr := parseConfigObject(req.Input, "native.claude.permission.input")
		if p.eng.opts.requestPermission != nil && claudeKnownTool(req.ToolName) && inputErr == nil {
			id := req.ToolUseID
			if id == "" {
				id = "can_use_tool-" + key
			}
			decision, permissionErr = p.eng.opts.requestPermission(ctx, PermissionRequest{
				SessionID: p.acpSessionIDLocked(), ToolCallID: id, Title: req.ToolName, Name: strPtr(req.ToolName), Kind: claudeToolKind(req.ToolName),
				RawInput: append(json.RawMessage(nil), req.Input...), Meta: map[string]any{"x-acp-runtime-native-request": raw},
				Options: []PermissionOption{{ID: "allow", Name: "Allow", Kind: "allow_once"}, {ID: "deny", Name: "Deny", Kind: "reject_once"}},
			})
		}
		p.mu.Lock()
		valid := p.turn == turn && !turn.finished && !turn.cancelling && p.approvals[key] == token && !token.revoked && ctx.Err() == nil
		if p.approvals[key] == token {
			delete(p.approvals, key)
		}
		p.mu.Unlock()
		if valid && permissionErr == nil && decision.Outcome == "selected" && decision.OptionID == "allow" {
			p.writeControlResponseWithInput(requestID, "allow", "", input)
		} else {
			p.writeControlResponse(requestID, "deny", "denied by host authority")
		}
	}()
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
	if err := p.writeLine(data); err != nil {
		p.failWrite(err)
	}
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
				Name:          &title, RawInput: append(json.RawMessage(nil), block.Input...),
				Kind:   strPtr(claudeToolKind(block.Name)),
				Status: &pending,
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
func (p *claudeProc) kill(ctx context.Context) error {
	p.cancelPermissions()
	p.mu.Lock()
	cmd, stdin, loopOn := p.cmd, p.stdin, p.loopOn
	mcpFile, settingsFile := p.mcpFile, p.settingsFile
	p.mu.Unlock()
	if loopOn != nil {
		defer loopOn()
	}
	if err := stopNativeProcess(ctx, cmd, stdin, &p.wait); err != nil {
		return err
	}
	if mcpFile != "" {
		_ = os.Remove(mcpFile)
	}
	if settingsFile != "" {
		_ = os.Remove(settingsFile)
	}
	return nil
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
	case "Bash":
		return "execute"
	case "Read":
		return "read"
	case "Write", "Edit", "NotebookEdit":
		return "edit"
	case "WebFetch":
		return "fetch"
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
	if list, ok := v.([]string); ok {
		return append([]string(nil), list...)
	}
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

// A single bounded writer keeps permission and cancellation frames from
// blocking decoding or interleaving JSON. Closing stdin interrupts its Write.
func (p *claudeProc) writeLine(data []byte) error {
	p.mu.Lock()
	stdin, queue, done := p.stdin, p.writeQueue, p.exitDone
	p.mu.Unlock()
	if stdin == nil {
		return io.ErrClosedPipe
	}
	data = append([]byte(nil), data...)
	if queue != nil {
		select {
		case <-done:
			return io.ErrClosedPipe
		default:
		}
		select {
		case queue <- data:
			return nil
		default:
			return wrapError(ErrorProcess, "native.claude.write", "native write queue is full", nil)
		}
	}
	// Unit fixtures that do not spawn a process can provide a direct writer.
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	_, err := stdin.Write(append(data, '\n'))
	return err
}
func (p *claudeProc) failWrite(err error) {
	p.mu.Lock()
	p.initErr = wrapError(ErrorProcess, "native.claude.write", "native transport write failed", err)
	if p.turn != nil && !p.turn.finished {
		p.turn.err = p.initErr
		p.turn.finished = true
		if p.turn.cancelPermission != nil {
			p.turn.cancelPermission()
		}
		close(p.turn.done)
	}
	stdin := p.stdin
	p.mu.Unlock()
	if stdin != nil {
		_ = stdin.Close()
	}
}

func claudeKnownTool(name string) bool {
	if claudeToolKind(name) != "other" {
		return true
	}
	switch name {
	case "Task", "Agent", "Skill", "TodoWrite", "AskUserQuestion":
		return true
	default:
		return false
	}
}
