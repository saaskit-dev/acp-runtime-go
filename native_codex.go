package acpruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// codexNativeEngine drives codex app-server DAEMONS, pooled by config
// fingerprint: sessions with the SAME configuration (CODEX_CONFIG + MCP
// servers + model) share one long-lived app-server process, so opening a new
// session is a millisecond-level thread/start RPC on a warm daemon. Sessions
// with a DIFFERENT configuration get their own process — per-config
// isolation without sacrificing daemon reuse.
//
// Protocol (verified against codex-cli 0.153.4 by live probe; RFC-0006):
// initialize -> initialized -> thread/start -> turn/start -> events ->
// turn/completed. Codex 0.153.4 requires wire_api=responses (chat removed).
type codexNativeEngine struct {
	opts     nativeEngineOptions
	mu       sync.Mutex
	daemons  map[string]*codexDaemon  // config key -> shared daemon
	sessions map[string]*codexSession // ACP session id (= thread id) -> ref
	closed   bool
}

func (e *codexNativeEngine) Name() string { return "codex" }

func (e *codexNativeEngine) Start(ctx context.Context, opts nativeEngineOptions) error {
	e.opts = opts
	if _, err := exec.LookPath(opts.Agent.Command); err != nil {
		return wrapError(ErrorProcess, "native.codex.spawn", "codex binary not found on PATH", err)
	}
	e.mu.Lock()
	e.daemons = map[string]*codexDaemon{}
	e.sessions = map[string]*codexSession{}
	e.closed = false
	e.mu.Unlock()
	return nil
}

// codexDaemon is one long-lived app-server process shared by every session
// with the same configuration fingerprint.
type codexDaemon struct {
	key    string
	eng    *codexNativeEngine
	mu     sync.Mutex
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	peer   *Peer
	loopOn context.CancelFunc
	turns  map[string]*codexTurn // threadID -> in-flight turn
	wait   nativeProcessWait
}

// codexSession ties an ACP session id to its daemon + thread.
type codexSession struct {
	daemon   *codexDaemon
	threadID string
	model    string
}

// daemonKey fingerprints everything that is process-scoped for codex: the
// CODEX_CONFIG translation, the session's MCP servers, and the model. Two
// sessions with the same key share one daemon; different keys get isolated
// processes.
func (e *codexNativeEngine) daemonKey(meta map[string]any, servers []MCPServer) string {
	hash := sha256.New()
	hash.Write([]byte(e.opts.Agent.Env["CODEX_CONFIG"]))
	mcpJSON, _ := json.Marshal(codexMCPOverrideArgs(servers))
	hash.Write(mcpJSON)
	if m, ok := meta["model"].(string); ok {
		hash.Write([]byte("|model=" + m))
	}
	return hex.EncodeToString(hash.Sum(nil))[:16]
}

func (e *codexNativeEngine) SessionConfigOptions() []SessionConfigOption {
	// Model/effort are daemon-scoped on the native path: set them through
	// CODEX_CONFIG (env) or the session Meta "model" key. Advertising config
	// options here would make InitialConfig look supported when the value
	// cannot change an already-running daemon.
	return nil
}

func (e *codexNativeEngine) NewSession(ctx context.Context, opts nativeEngineOptions, req NewSessionRequest) (string, error) {
	key := e.daemonKey(req.Meta, req.MCPServers)
	daemon, err := e.ensureDaemon(ctx, key, req.MCPServers)
	if err != nil {
		return "", err
	}
	model := nativeCodexModel(opts.Agent, req.Meta)
	var res struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
		Model string `json:"model"`
	}
	if err := daemon.peer.Call(ctx, "thread/start", map[string]any{
		"cwd":                   opts.CWD,
		"model":                 model,
		"experimentalRawEvents": true,
	}, &res); err != nil {
		return "", wrapError(ErrorProcess, "native.codex.thread", "thread/start failed", err)
	}
	if res.Thread.ID == "" {
		return "", &RuntimeError{Kind: ErrorProcess, Op: "native.codex.thread", Msg: "thread/start returned no thread id"}
	}
	if model != "" && res.Model == "" {
		return "", wrapError(ErrorProtocol, "native.codex.thread", "thread/start did not report the selected model", nil)
	}
	e.mu.Lock()
	e.sessions[res.Thread.ID] = &codexSession{daemon: daemon, threadID: res.Thread.ID, model: res.Model}
	e.mu.Unlock()
	return res.Thread.ID, nil
}

func (e *codexNativeEngine) LoadSession(ctx context.Context, opts nativeEngineOptions, req LoadSessionRequest) (string, error) {
	e.mu.Lock()
	if s, ok := e.sessions[req.SessionID]; ok && s.daemon.alive() {
		e.mu.Unlock()
		return req.SessionID, nil
	}
	e.mu.Unlock()
	key := e.daemonKey(nil, req.MCPServers)
	daemon, err := e.ensureDaemon(ctx, key, req.MCPServers)
	if err != nil {
		return "", err
	}
	var res struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
		Model string `json:"model"`
	}
	if err := daemon.peer.Call(ctx, "thread/resume", map[string]any{
		"threadId":              req.SessionID,
		"cwd":                   opts.CWD,
		"model":                 nativeCodexModel(opts.Agent, req.Meta),
		"experimentalRawEvents": true,
	}, &res); err != nil {
		return "", wrapError(ErrorProcess, "native.codex.resume", "thread/resume failed", err)
	}
	e.mu.Lock()
	e.sessions[req.SessionID] = &codexSession{daemon: daemon, threadID: req.SessionID, model: res.Model}
	e.mu.Unlock()
	return req.SessionID, nil
}

// ensureDaemon returns the shared daemon for a config key, spawning it on
// first use.
func (e *codexNativeEngine) ensureDaemon(ctx context.Context, key string, servers []MCPServer) (*codexDaemon, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil, wrapError(ErrorSessionClosed, "native.codex.spawn", "native connection is closed", nil)
	}
	if e.daemons == nil {
		e.daemons = map[string]*codexDaemon{}
	}
	if d, ok := e.daemons[key]; ok {
		return d, nil
	}
	// ponytail: serialize cold daemon admission per engine; narrow only if
	// concurrent cold starts on one connection become a measured bottleneck.
	d := &codexDaemon{key: key, eng: e, turns: map[string]*codexTurn{}}
	if err := d.spawn(ctx, codexSpawnExtras(e.opts, servers)); err != nil {
		_ = d.kill(context.Background())
		return nil, err
	}
	e.daemons[key] = d
	return d, nil
}

func (e *codexNativeEngine) Prompt(ctx context.Context, opts nativeEngineOptions, sessionID string, blocks []ContentBlock) (nativeTurnResult, error) {
	e.mu.Lock()
	s, ok := e.sessions[sessionID]
	e.mu.Unlock()
	if !ok {
		return nativeTurnResult{}, &RuntimeError{Kind: ErrorProcess, Op: "native.codex.turn", Msg: "no session " + sessionID + " on this connection"}
	}
	return s.daemon.prompt(ctx, s.threadID, blocks)
}

func (e *codexNativeEngine) Cancel(ctx context.Context, opts nativeEngineOptions, sessionID string) {
	e.mu.Lock()
	s, ok := e.sessions[sessionID]
	e.mu.Unlock()
	if !ok {
		return
	}
	s.daemon.interrupt(s.threadID)
}

func (e *codexNativeEngine) Close(ctx context.Context) error {
	e.mu.Lock()
	e.closed = true
	daemons := make(map[string]*codexDaemon, len(e.daemons))
	for key, daemon := range e.daemons {
		daemons[key] = daemon
	}
	e.mu.Unlock()
	var cleanupErrors []error
	for key, daemon := range daemons {
		if err := daemon.kill(ctx); err != nil {
			cleanupErrors = append(cleanupErrors, err)
			continue
		}
		e.mu.Lock()
		delete(e.daemons, key)
		for id, session := range e.sessions {
			if session.daemon == daemon {
				delete(e.sessions, id)
			}
		}
		e.mu.Unlock()
	}
	return errors.Join(cleanupErrors...)
}

// codexProc (kept name for tests) is the per-daemon process state.
type codexProc = codexDaemon

// codexDaemon process state and protocol handling.

// codexDaemon process state and protocol handling (methods).
func (d *codexDaemon) alive() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.cmd != nil && d.cmd.Process != nil
}

// spawn launches the app-server with config/-c overrides and completes the
// initialize handshake.
func (d *codexDaemon) spawn(ctx context.Context, extraArgs []string) error {
	opts := d.eng.opts
	cmd := exec.Command(opts.Agent.Command, append(opts.Agent.Args, extraArgs...)...)
	configureProcessGroup(cmd)
	cmd.Dir = opts.CWD
	cmd.Env = envSlice(opts.Agent.Env)
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return wrapError(ErrorProcess, "native.codex.spawn", "stdin pipe", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return wrapError(ErrorProcess, "native.codex.spawn", "stdout pipe", err)
	}
	if err := cmd.Start(); err != nil {
		return wrapError(ErrorProcess, "native.codex.spawn", "failed to spawn codex app-server", err)
	}
	d.mu.Lock()
	d.cmd, d.stdin = cmd, stdin
	d.mu.Unlock()

	loopCtx, cancelLoop := context.WithCancel(context.Background())
	d.loopOn = cancelLoop
	d.peer = NewPeer(stdout, stdin, PeerOptions{})
	d.registerHandlers()
	go func() { _ = d.peer.Start(loopCtx) }()

	var initResp json.RawMessage
	if err := d.peer.Call(ctx, "initialize", map[string]any{
		"clientInfo":   map[string]any{"name": "acp-runtime-go", "version": "0.1.0"},
		"capabilities": map[string]any{"experimentalApi": true},
	}, &initResp); err != nil {
		return wrapError(ErrorProcess, "native.codex.initialize", "app-server handshake failed", err)
	}
	if err := d.peer.Notify(ctx, "initialized", map[string]any{}); err != nil {
		return wrapError(ErrorProcess, "native.codex.initialize", "initialized notify failed", err)
	}
	return nil
}

func (d *codexDaemon) registerHandlers() {
	peer := d.peer
	peer.RegisterNotification("item/agentMessage/delta", func(ctx context.Context, raw json.RawMessage) {
		var ev struct {
			ThreadID string `json:"threadId"`
			ItemID   string `json:"itemId"`
			Delta    string `json:"delta"`
		}
		if err := json.Unmarshal(raw, &ev); err != nil || ev.Delta == "" {
			return
		}
		d.mu.Lock()
		turn := d.turns[ev.ThreadID]
		if turn != nil {
			turn.deltas[ev.ItemID] = true
		}
		d.mu.Unlock()
		if turn != nil {
			d.emit(ev.ThreadID, SessionUpdate{SessionUpdate: "agent_message_chunk", MessageID: ev.ItemID, Text: ev.Delta})
		}
	})
	for _, method := range []string{"item/reasoning/textDelta", "item/reasoning/summaryTextDelta"} {
		peer.RegisterNotification(method, func(ctx context.Context, raw json.RawMessage) {
			var ev struct {
				ThreadID string `json:"threadId"`
				Delta    string `json:"delta"`
			}
			if err := json.Unmarshal(raw, &ev); err != nil || ev.Delta == "" {
				return
			}
			d.emit(ev.ThreadID, SessionUpdate{SessionUpdate: "agent_thought_chunk", Text: ev.Delta})
		})
	}
	peer.RegisterNotification("item/started", func(ctx context.Context, raw json.RawMessage) {
		var ev struct {
			ThreadID string `json:"threadId"`
			Item     struct {
				Type    string `json:"type"`
				ID      string `json:"id"`
				Command string `json:"command"`
			} `json:"item"`
		}
		if err := json.Unmarshal(raw, &ev); err != nil {
			return
		}
		if ev.Item.Type != "commandExecution" {
			return
		}
		title := ev.Item.Command
		if len(title) > 200 {
			title = title[:200]
		}
		pending := "pending"
		d.emit(ev.ThreadID, SessionUpdate{SessionUpdate: "tool_call", ToolCallID: ev.Item.ID,
			Title: &title, Kind: strPtr("execute_command"), Status: &pending})
	})
	peer.RegisterNotification("item/completed", func(ctx context.Context, raw json.RawMessage) {
		var ev struct {
			ThreadID string `json:"threadId"`
			Item     struct {
				Type string `json:"type"`
				ID   string `json:"id"`
				Text string `json:"text"`
			} `json:"item"`
		}
		if err := json.Unmarshal(raw, &ev); err != nil {
			return
		}
		switch ev.Item.Type {
		case "commandExecution":
			completed := "completed"
			d.emit(ev.ThreadID, SessionUpdate{SessionUpdate: "tool_call_update", ToolCallID: ev.Item.ID, Status: &completed})
		case "agentMessage":
			d.mu.Lock()
			turn := d.turns[ev.ThreadID]
			deltas := turn != nil && turn.deltas[ev.Item.ID]
			d.mu.Unlock()
			if !deltas && strings.TrimSpace(ev.Item.Text) != "" {
				d.emit(ev.ThreadID, SessionUpdate{SessionUpdate: "agent_message_chunk", MessageID: ev.Item.ID, Text: ev.Item.Text})
			}
		}
	})
	peer.RegisterNotification("rawResponse/completed", func(ctx context.Context, raw json.RawMessage) {
		var ev struct {
			ThreadID string `json:"threadId"`
			Usage    struct {
				TotalTokens           uint64 `json:"totalTokens"`
				InputTokens           uint64 `json:"inputTokens"`
				CachedInputTokens     uint64 `json:"cachedInputTokens"`
				CacheWriteInputTokens uint64 `json:"cacheWriteInputTokens"`
				OutputTokens          uint64 `json:"outputTokens"`
				ReasoningOutputTokens uint64 `json:"reasoningOutputTokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(raw, &ev); err != nil {
			return
		}
		cached := ev.Usage.CachedInputTokens
		cacheWrite := ev.Usage.CacheWriteInputTokens
		output := ev.Usage.OutputTokens
		thought := ev.Usage.ReasoningOutputTokens
		d.mu.Lock()
		if turn := d.turns[ev.ThreadID]; turn != nil {
			turn.usage = &Usage{
				TotalTokens:       ev.Usage.TotalTokens,
				InputTokens:       ev.Usage.InputTokens,
				OutputTokens:      output,
				ThoughtTokens:     &thought,
				CachedReadTokens:  &cached,
				CachedWriteTokens: &cacheWrite,
			}
		}
		d.mu.Unlock()
	})
	peer.RegisterNotification("turn/completed", func(ctx context.Context, raw json.RawMessage) {
		var ev struct {
			ThreadID string `json:"threadId"`
			Turn     struct {
				Status string `json:"status"`
				Error  *struct {
					Message string `json:"message"`
				} `json:"error"`
			} `json:"turn"`
		}
		if err := json.Unmarshal(raw, &ev); err != nil {
			return
		}
		d.settleTurn(ev.ThreadID, "end_turn", nil)
		if ev.Turn.Status == "failed" {
			msg := "codex turn failed"
			if ev.Turn.Error != nil && ev.Turn.Error.Message != "" {
				msg = ev.Turn.Error.Message
			}
			d.settleTurn(ev.ThreadID, "end_turn", fmt.Errorf("%s", msg))
		}
	})
	peer.RegisterNotification("error", func(ctx context.Context, raw json.RawMessage) {
		var ev struct {
			ThreadID string `json:"threadId"`
			Message  string `json:"message"`
			Error    *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(raw, &ev); err != nil {
			return
		}
		msg := ev.Message
		if msg == "" && ev.Error != nil {
			msg = ev.Error.Message
		}
		if msg == "" || codexTransientError(msg) {
			return
		}
		d.mu.Lock()
		if turn := d.turns[ev.ThreadID]; turn != nil && !turn.finished {
			turn.finished = true
			turn.err = fmt.Errorf("%s", msg)
			close(turn.done)
		}
		d.mu.Unlock()
	})
	d.registerApprovals(peer)
}

// settleTurn resolves the in-flight turn. A non-nil err marks it failed;
// calling twice (error then turn/completed) is a no-op.
func (d *codexDaemon) settleTurn(threadID, stop string, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	turn := d.turns[threadID]
	if turn == nil || turn.finished {
		return
	}
	turn.finished = true
	if err != nil {
		turn.err = err
	} else {
		turn.stop = stop
	}
	close(turn.done)
}

func (d *codexDaemon) emit(threadID string, update SessionUpdate) {
	d.eng.opts.emit(threadID, update)
}

func (d *codexDaemon) prompt(ctx context.Context, threadID string, blocks []ContentBlock) (nativeTurnResult, error) {
	d.mu.Lock()
	if turn := d.turns[threadID]; turn != nil && !turn.finished {
		d.mu.Unlock()
		return nativeTurnResult{}, &RuntimeError{Kind: ErrorProcess, Op: "native.codex.turn", Msg: "a turn is already in flight"}
	}
	turn := &codexTurn{done: make(chan struct{}), stop: "end_turn", deltas: map[string]bool{}}
	d.turns[threadID] = turn
	d.mu.Unlock()

	var startRes struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := d.peer.Call(ctx, "turn/start", map[string]any{
		"threadId": threadID,
		"input":    codexInputFromBlocks(blocks),
	}, &startRes); err != nil {
		d.mu.Lock()
		delete(d.turns, threadID)
		d.mu.Unlock()
		return nativeTurnResult{}, wrapError(ErrorProcess, "native.codex.turn", "turn/start failed", err)
	}
	_ = startRes

	select {
	case <-turnDone(d, threadID):
	case <-d.peer.Done():
		select {
		case <-turn.done:
		default:
			return nativeTurnResult{}, wrapError(ErrorProcess, "native.codex.turn", "Codex app-server closed before a terminal result", io.ErrClosedPipe)
		}
	case <-ctx.Done():
		return nativeTurnResult{}, ctx.Err()
	}
	d.mu.Lock()
	err, stop, usage := turn.err, turn.stop, turn.usage
	d.mu.Unlock()
	if err != nil {
		return nativeTurnResult{}, &RuntimeError{Kind: ErrorProcess, Op: "native.codex.turn", Msg: err.Error()}
	}
	return nativeTurnResult{StopReason: stop, Usage: usage}, nil
}

func turnDone(d *codexDaemon, threadID string) <-chan struct{} {
	d.mu.Lock()
	defer d.mu.Unlock()
	if turn := d.turns[threadID]; turn != nil {
		return turn.done
	}
	closed := make(chan struct{})
	close(closed)
	return closed
}

// interrupt best-effort stops the in-flight turn. turn/interrupt is not in
// any public schema; failures are ignored — the turn settles via
// turn/completed (or the caller's context).
func (d *codexDaemon) interrupt(threadID string) {
	d.mu.Lock()
	turn := d.turns[threadID]
	d.mu.Unlock()
	if turn == nil || turn.finished {
		return
	}
	_ = d.peer.Notify(context.Background(), "turn/interrupt", map[string]any{"threadId": threadID, "turnId": turn.id})
}

// registerApprovals maps codex's server-initiated approval requests (naming
// varies across CLI versions — all known shapes handled; response is flat
// {"decision": ...}, verify on major upgrades) onto the ACP
// session/request_permission round-trip. Failure to answer fails CLOSED.
func (d *codexDaemon) registerApprovals(peer *Peer) {
	for _, method := range []string{"execCommandApproval", "exec_approval_request", "applyPatchApproval"} {
		peer.RegisterRequest(method, func(ctx context.Context, raw json.RawMessage) (any, error) {
			return d.handleApproval(ctx, raw)
		})
	}
}

func (d *codexDaemon) handleApproval(ctx context.Context, raw json.RawMessage) (any, error) {
	var req struct {
		ThreadID string   `json:"threadId"`
		CallID   string   `json:"callId"`
		Command  []string `json:"command"`
		Reason   string   `json:"reason"`
		Title    string   `json:"title"`
	}
	_ = json.Unmarshal(raw, &req)
	if d.eng.opts.requestPermission == nil {
		return map[string]any{"decision": "denied"}, nil
	}
	title := strings.Join(req.Command, " ")
	if title == "" {
		title = req.Title
	}
	if title == "" {
		title = req.Reason
	}
	decision, err := d.eng.opts.requestPermission(ctx, PermissionRequest{
		SessionID:  req.ThreadID,
		ToolCallID: req.CallID,
		Title:      title,
		Kind:       "execute_command",
		Options: []PermissionOption{
			{ID: "approve", Name: "Approve", Kind: "allow_once"},
			{ID: "deny", Name: "Deny", Kind: "reject_once"},
		},
	})
	if err != nil {
		return map[string]any{"decision": "denied"}, nil
	}
	if decision.Outcome == "selected" && decision.OptionID == "approve" {
		return map[string]any{"decision": "approved"}, nil
	}
	return map[string]any{"decision": "denied"}, nil
}

// kill terminates the daemon process tree.
func (d *codexDaemon) kill(ctx context.Context) error {
	d.mu.Lock()
	cmd, stdin, loopOn := d.cmd, d.stdin, d.loopOn
	d.mu.Unlock()
	if loopOn != nil {
		defer loopOn()
	}
	if d.peer != nil {
		d.peer.Close()
	}
	return stopNativeProcess(ctx, cmd, stdin, &d.wait)
}

func strPtr(s string) *string { return &s }

// codexSpawnExtras combines the CODEX_CONFIG translation and MCP server
// overrides for a daemon's argv.
func codexSpawnExtras(opts nativeEngineOptions, servers []MCPServer) []string {
	extras := codexConfigOverrideArgs(opts.Agent.Env)
	return append(extras, codexMCPOverrideArgs(servers)...)
}

// codexTransientError reports whether an error notification is a provider
// retry notice codex is already recovering from.
func codexTransientError(msg string) bool {
	patterns := []string{"reconnecting", "retrying", "retry ", "stream disconnected"}
	lower := strings.ToLower(msg)
	for _, pattern := range patterns {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	return false
}

// codexTurn tracks one in-flight turn on a thread.
type codexTurn struct {
	id       string
	done     chan struct{}
	stop     string
	usage    *Usage
	err      error
	deltas   map[string]bool // item ids whose text deltas were already emitted
	finished bool
}

// metaString extracts a string value from session metadata.
func metaString(meta map[string]any, key string) string {
	if meta == nil {
		return ""
	}
	v, _ := meta[key].(string)
	return v
}

// codexConfigOverrideArgs translates the CODEX_CONFIG env JSON (the project
// convention shared with the ACP wrapper path) into codex -c key=value argv
// overrides. Nested maps flatten to TOML dotted paths (codex -c rejects JSON
// inline tables for map values). Scalars convert to TOML literals.
func codexConfigOverrideArgs(env map[string]string) []string {
	configJSON := env["CODEX_CONFIG"]
	if strings.TrimSpace(configJSON) == "" {
		return nil
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		return nil // invalid JSON: leave the CLI with its default config
	}
	var args []string
	flattenCodexConfig("", cfg, &args)
	return args
}

func flattenCodexConfig(prefix string, value any, args *[]string) {
	if m, ok := value.(map[string]any); ok {
		keys := make([]string, 0, len(m))
		for key := range m {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			path := key
			if prefix != "" {
				path = prefix + "." + key
			}
			flattenCodexConfig(path, m[key], args)
		}
		return
	}
	if prefix == "" {
		return
	}
	*args = append(*args, "-c", prefix+"="+tomlValueOf(value))
}

func tomlValueOf(v any) string {
	switch t := v.(type) {
	case string:
		return strconv.Quote(t)
	case bool:
		if t {
			return "true"
		}
		return "false"
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		data, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		return string(data)
	}
}

// codexMCPOverrideArgs materializes MCP servers as mcp_servers.* config
// overrides (dotted -c paths): stdio via command/args/env, HTTP via url and
// per-server http_headers.
func codexMCPOverrideArgs(servers []MCPServer) []string {
	var args []string
	for _, server := range servers {
		if server.Name == "" {
			continue
		}
		base := "mcp_servers." + server.Name
		switch {
		case server.Command != "":
			args = append(args, "-c", base+".command="+strconv.Quote(server.Command))
			if len(server.Args) > 0 {
				data, _ := json.Marshal(server.Args)
				args = append(args, "-c", base+".args="+string(data))
			}
			if len(server.Env) > 0 {
				env := map[string]string{}
				for _, item := range server.Env {
					env[item.Name] = item.Value
				}
				data, _ := json.Marshal(env)
				args = append(args, "-c", base+".env="+string(data))
			}
		case server.URL != "":
			args = append(args, "-c", base+".url="+strconv.Quote(server.URL))
			for _, header := range server.Headers {
				args = append(args, "-c", base+".http_headers."+header.Name+"="+strconv.Quote(header.Value))
			}
		}
	}
	return args
}

// codexInputFromBlocks maps ACP prompt blocks onto codex input items (text
// and base64/url images; resources degrade to text; audio is unsupported by
// the engine and skipped).
func codexInputFromBlocks(blocks []ContentBlock) []any {
	items := make([]any, 0, len(blocks))
	appendText := func(text string) {
		items = append(items, map[string]any{"type": "text", "text": text})
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
				items = append(items, map[string]any{"type": "input_image",
					"image_url": "data:" + mime + ";base64," + block.Data})
			case block.URI != "":
				items = append(items, map[string]any{"type": "input_image", "image_url": block.URI})
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
			// codex accepts no audio input; skipped deliberately.
		}
	}
	return items
}
