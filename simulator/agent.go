package simulator

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	acp "github.com/saaskit-dev/acp-runtime-go"
)

type AuthMode string

const (
	AuthNone     AuthMode = "none"
	AuthOptional AuthMode = "optional"
	AuthRequired AuthMode = "required"

	runPendingStatus = "pending"
)

type Options struct {
	AuthMode   AuthMode
	Name       string
	Title      string
	Version    string
	StorageDir string
}

type Agent struct {
	options Options
	peer    *acp.Peer

	mu                 sync.Mutex
	sessions           map[string]*Session
	clientCapabilities acp.ClientCapabilities
	turns              map[string]context.CancelFunc
}

type Session struct {
	ID                    string                    `json:"id"`
	CWD                   string                    `json:"cwd"`
	AdditionalDirectories []string                  `json:"additionalDirectories,omitempty"`
	MCPServers            []acp.MCPServer           `json:"mcpServers,omitempty"`
	Title                 string                    `json:"title,omitempty"`
	UpdatedAt             string                    `json:"updatedAt"`
	Modes                 acp.SessionModeState      `json:"modes"`
	ConfigOptions         []acp.SessionConfigOption `json:"configOptions"`
	History               []acp.SessionNotification `json:"history,omitempty"`
	PendingFaults         []string                  `json:"pendingFaults,omitempty"`
}

func New(options Options) *Agent {
	if options.AuthMode == "" {
		options.AuthMode = AuthNone
	}
	if options.Name == "" {
		options.Name = "simulator-agent-acp"
	}
	if options.Title == "" {
		options.Title = "Simulator Agent ACP"
	}
	if options.Version == "" {
		options.Version = "0.1.0"
	}
	if options.StorageDir == "" {
		options.StorageDir = filepath.Join(os.TempDir(), "acp-simulator-agent")
	}
	return &Agent{options: options, sessions: map[string]*Session{}, turns: map[string]context.CancelFunc{}}
}

func RunStdio(ctx context.Context, stdin io.Reader, stdout io.Writer, options Options) error {
	agent := New(options)
	peer := acp.NewPeer(stdin, stdout, acp.PeerOptions{})
	agent.peer = peer
	agent.register(peer)
	return peer.Start(ctx)
}

func (a *Agent) register(peer *acp.Peer) {
	peer.RegisterRequest("initialize", a.handleInitialize)
	peer.RegisterRequest("authenticate", a.handleAuthenticate)
	peer.RegisterRequest("session/new", a.handleNewSession)
	peer.RegisterRequest("session/list", a.handleListSessions)
	peer.RegisterRequest("session/load", a.handleLoadSession)
	peer.RegisterRequest("session/resume", a.handleResumeSession)
	peer.RegisterRequest("session/fork", a.handleForkSession)
	peer.RegisterRequest("session/prompt", a.handlePrompt)
	peer.RegisterRequest("session/set_mode", a.handleSetMode)
	peer.RegisterRequest("session/set_config_option", a.handleSetConfigOption)
	peer.RegisterRequest("session/close", a.handleCloseSession)
	peer.RegisterRequest("session/delete", a.handleDeleteSession)
	peer.RegisterRequest("logout", a.handleLogout)
	peer.RegisterNotification("session/cancel", func(_ context.Context, raw json.RawMessage) {
		var req struct {
			SessionID string `json:"sessionId"`
		}
		if json.Unmarshal(raw, &req) != nil {
			return
		}
		a.mu.Lock()
		cancel := a.turns[req.SessionID]
		a.mu.Unlock()
		if cancel != nil {
			cancel()
		}
	})
}

func (a *Agent) handleInitialize(ctx context.Context, raw json.RawMessage) (any, error) {
	var req acp.InitializeRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, invalidParams(err.Error())
	}
	if req.ProtocolVersion != acp.ProtocolVersion {
		return nil, invalidParams(fmt.Sprintf("unsupported protocolVersion %d", req.ProtocolVersion))
	}
	a.mu.Lock()
	a.clientCapabilities = req.ClientCapabilities
	a.mu.Unlock()
	resp := acp.InitializeResponse{
		ProtocolVersion: acp.ProtocolVersion,
		AgentInfo:       &acp.Implementation{Name: a.options.Name, Version: a.options.Version},
		AgentCapabilities: acp.AgentCapabilities{
			LoadSession:        true,
			Auth:               map[string]any{"logout": map[string]any{}},
			PromptCapabilities: acp.PromptCapabilities{EmbeddedContext: true, Image: false, Audio: false},
			MCPCapabilities:    acp.MCPCapabilities{HTTP: true, SSE: true},
			SessionCapabilities: acp.SessionCapabilities{
				Close:                 map[string]any{},
				Delete:                map[string]any{},
				List:                  map[string]any{},
				Resume:                map[string]any{},
				AdditionalDirectories: map[string]any{},
			},
		},
	}
	if a.options.AuthMode != AuthNone {
		resp.AuthMethods = []acp.AuthMethod{{Type: "agent", ID: "simulator-agent-login", Name: "Simulator Agent"}}
	}
	return resp, nil
}

func (a *Agent) handleAuthenticate(ctx context.Context, raw json.RawMessage) (any, error) {
	if a.options.AuthMode == AuthNone {
		return acp.AuthenticateResponse{}, nil
	}
	var req acp.AuthenticateRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, invalidParams(err.Error())
	}
	if req.MethodID == "" || req.MethodID == "simulator-agent-login" {
		return acp.AuthenticateResponse{}, nil
	}
	return nil, invalidParams("unknown auth method")
}

func (a *Agent) handleNewSession(ctx context.Context, raw json.RawMessage) (any, error) {
	var req acp.NewSessionRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, invalidParams(err.Error())
	}
	if !filepath.IsAbs(req.CWD) {
		return nil, invalidParams("cwd must be absolute")
	}
	session := a.createSession(req.CWD, req.AdditionalDirectories, req.MCPServers)
	a.save(session)
	if err := a.notify(ctx, session.ID, acp.SessionUpdate{SessionUpdate: "available_commands_update", AvailableCommands: defaultCommands()}); err != nil {
		return nil, err
	}
	return a.sessionResponse(session), nil
}

func (a *Agent) handleLoadSession(ctx context.Context, raw json.RawMessage) (any, error) {
	var req acp.LoadSessionRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, invalidParams(err.Error())
	}
	session, err := a.findSession(req.SessionID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"modes": session.Modes, "configOptions": session.ConfigOptions}, nil
}

func (a *Agent) handleResumeSession(ctx context.Context, raw json.RawMessage) (any, error) {
	var req acp.ResumeSessionRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, invalidParams(err.Error())
	}
	session, err := a.findSession(req.SessionID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"modes": session.Modes, "configOptions": session.ConfigOptions}, nil
}

func (a *Agent) handleForkSession(ctx context.Context, raw json.RawMessage) (any, error) {
	var req acp.ForkSessionRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, invalidParams(err.Error())
	}
	source, err := a.findSession(req.SessionID)
	if err != nil {
		return nil, err
	}
	session := *source
	session.ID = newID()
	session.Title = "Fork of " + source.ID
	session.UpdatedAt = now()
	a.save(&session)
	return a.sessionResponse(&session), nil
}

func (a *Agent) handleListSessions(ctx context.Context, raw json.RawMessage) (any, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	sessions := make([]acp.SessionInfo, 0, len(a.sessions))
	for _, session := range a.sessions {
		title := session.Title
		updated := session.UpdatedAt
		sessions = append(sessions, acp.SessionInfo{
			SessionID:             session.ID,
			CWD:                   session.CWD,
			Title:                 &title,
			UpdatedAt:             &updated,
			AdditionalDirectories: append([]string(nil), session.AdditionalDirectories...),
		})
	}
	return acp.ListSessionsResponse{Sessions: sessions}, nil
}

func (a *Agent) handlePrompt(ctx context.Context, raw json.RawMessage) (any, error) {
	var req acp.PromptRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, invalidParams(err.Error())
	}
	session, err := a.findSession(req.SessionID)
	if err != nil {
		return nil, err
	}
	turnCtx, cancel := context.WithCancel(ctx)
	a.mu.Lock()
	if _, busy := a.turns[session.ID]; busy {
		a.mu.Unlock()
		cancel()
		return nil, invalidParams("session busy")
	}
	a.turns[session.ID] = cancel
	a.mu.Unlock()
	defer func() { cancel(); a.mu.Lock(); delete(a.turns, session.ID); a.mu.Unlock() }()
	text := promptText(req.Prompt)
	action := parseAction(text)
	if err := a.executeAction(turnCtx, session, action); err != nil {
		if turnCtx.Err() != nil {
			return acp.PromptResponse{StopReason: "cancelled"}, nil
		}
		return nil, err
	}
	if turnCtx.Err() != nil {
		return acp.PromptResponse{StopReason: "cancelled"}, nil
	}
	session.UpdatedAt = now()
	a.save(session)
	usage := &acp.Usage{TotalTokens: uint64(len(text) + 2), InputTokens: uint64(len(text)), OutputTokens: 2}
	used, size := uint64(len(text)), uint64(8192)
	_ = a.notify(ctx, session.ID, acp.SessionUpdate{SessionUpdate: "usage_update", Used: &used, Size: &size})
	return acp.PromptResponse{StopReason: "end_turn", Usage: usage, UserMessageID: req.MessageID}, nil
}

func (a *Agent) handleSetMode(ctx context.Context, raw json.RawMessage) (any, error) {
	var req acp.SetSessionModeRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, invalidParams(err.Error())
	}
	session, err := a.findSession(req.SessionID)
	if err != nil {
		return nil, err
	}
	found := false
	for _, mode := range session.Modes.AvailableModes {
		if mode.ID == req.ModeID {
			found = true
			break
		}
	}
	if !found {
		return nil, invalidParams("unknown mode")
	}
	session.Modes.CurrentModeID = req.ModeID
	session.UpdatedAt = now()
	a.save(session)
	_ = a.notify(ctx, session.ID, acp.SessionUpdate{SessionUpdate: "current_mode_update", CurrentModeID: req.ModeID})
	return acp.SetSessionModeResponse{}, nil
}

func (a *Agent) handleSetConfigOption(ctx context.Context, raw json.RawMessage) (any, error) {
	var req acp.SetSessionConfigOptionRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, invalidParams(err.Error())
	}
	session, err := a.findSession(req.SessionID)
	if err != nil {
		return nil, err
	}
	for i := range session.ConfigOptions {
		if session.ConfigOptions[i].ID == req.OptionID {
			session.ConfigOptions[i].Value = req.Value
			a.save(session)
			options := append([]acp.SessionConfigOption(nil), session.ConfigOptions...)
			_ = a.notify(ctx, session.ID, acp.SessionUpdate{SessionUpdate: "config_option_update", ConfigOptions: options})
			return acp.SetSessionConfigOptionResponse{ConfigOptions: &options}, nil
		}
	}
	return nil, invalidParams("unknown config option")
}

func (a *Agent) handleCloseSession(ctx context.Context, raw json.RawMessage) (any, error) {
	var req acp.CloseSessionRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, invalidParams(err.Error())
	}
	a.mu.Lock()
	delete(a.sessions, req.SessionID)
	a.mu.Unlock()
	return acp.CloseSessionResponse{}, nil
}

// handleDeleteSession implements session/delete: remove the session from
// persistent storage so it can no longer be loaded/resumed.
func (a *Agent) handleDeleteSession(ctx context.Context, raw json.RawMessage) (any, error) {
	var req acp.DeleteSessionRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, invalidParams(err.Error())
	}
	a.mu.Lock()
	delete(a.sessions, req.SessionID)
	a.mu.Unlock()
	_ = os.Remove(filepath.Join(a.options.StorageDir, req.SessionID+".json"))
	return acp.DeleteSessionResponse{}, nil
}

// handleLogout implements logout: a no-op for the simulator since it holds no
// real credentials, but it must be implemented for protocol completeness.
func (a *Agent) handleLogout(ctx context.Context, raw json.RawMessage) (any, error) {
	return acp.LogoutResponse{}, nil
}

func (a *Agent) createSession(cwd string, additional []string, mcp []acp.MCPServer) *Session {
	return &Session{
		ID:                    newID(),
		CWD:                   cwd,
		AdditionalDirectories: append([]string(nil), additional...),
		MCPServers:            append([]acp.MCPServer(nil), mcp...),
		Title:                 "Simulator Session",
		UpdatedAt:             now(),
		Modes:                 defaultModes(),
		ConfigOptions:         defaultConfigOptions(),
	}
}

func (a *Agent) sessionResponse(session *Session) acp.NewSessionResponse {
	return acp.NewSessionResponse{
		SessionID:     session.ID,
		Modes:         &session.Modes,
		ConfigOptions: append([]acp.SessionConfigOption(nil), session.ConfigOptions...),
	}
}

func (a *Agent) save(session *Session) {
	a.mu.Lock()
	a.sessions[session.ID] = session
	a.mu.Unlock()
	_ = os.MkdirAll(a.options.StorageDir, 0o755)
	bytes, err := json.MarshalIndent(session, "", "  ")
	if err == nil {
		_ = os.WriteFile(filepath.Join(a.options.StorageDir, session.ID+".json"), bytes, 0o644)
	}
}

func (a *Agent) findSession(id string) (*Session, error) {
	a.mu.Lock()
	if session := a.sessions[id]; session != nil {
		a.mu.Unlock()
		return session, nil
	}
	a.mu.Unlock()
	bytes, err := os.ReadFile(filepath.Join(a.options.StorageDir, id+".json"))
	if err != nil {
		return nil, invalidParams("unknown session")
	}
	var session Session
	if err := json.Unmarshal(bytes, &session); err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.sessions[id] = &session
	a.mu.Unlock()
	return &session, nil
}

func (a *Agent) notify(ctx context.Context, sessionID string, update acp.SessionUpdate) error {
	if update.SessionUpdate == "agent_message_chunk" || update.SessionUpdate == "agent_thought_chunk" || update.SessionUpdate == "user_message_chunk" {
		wire := map[string]any{"sessionUpdate": update.SessionUpdate, "content": map[string]any{"type": "text", "text": update.Text}}
		if update.MessageID != "" {
			wire["messageId"] = update.MessageID
		}
		return a.peer.Notify(ctx, "session/update", map[string]any{"sessionId": sessionID, "update": wire})
	}
	return a.peer.Notify(ctx, "session/update", acp.SessionNotification{SessionID: sessionID, Update: update})
}

// runViaTerminal performs a real terminal round-trip against the host:
// terminal/create -> terminal/wait_for_exit -> terminal/release. It returns
// the command's combined output and true on success, or "" / false if any RPC
// failed (caller falls back to a synthetic result).
func (a *Agent) runViaTerminal(ctx context.Context, sessionID, cwd, command string, args []string) (string, error) {
	var created struct {
		TerminalID string `json:"terminalId"`
	}
	if err := a.peer.Call(ctx, "terminal/create", map[string]any{"sessionId": sessionID, "command": command, "args": args, "cwd": cwd}, &created); err != nil {
		return "", err
	}
	if created.TerminalID == "" {
		return "", fmt.Errorf("terminal/create returned no ID")
	}
	params := map[string]any{"sessionId": sessionID, "terminalId": created.TerminalID}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		_ = a.peer.Call(cleanup, "terminal/release", params, nil)
	}()
	var exit struct {
		ExitCode *uint32 `json:"exitCode"`
		Signal   *string `json:"signal"`
	}
	if err := a.peer.Call(ctx, "terminal/wait_for_exit", params, &exit); err != nil {
		return "", err
	}
	if exit.ExitCode == nil || *exit.ExitCode != 0 {
		return "", fmt.Errorf("terminal exited unsuccessfully")
	}
	var output struct {
		Output string `json:"output"`
	}
	if err := a.peer.Call(ctx, "terminal/output", params, &output); err != nil {
		return "", err
	}
	return strings.TrimSpace(output.Output), nil
}

type action struct {
	kind    string
	path    string
	content string
	command string
	args    []string
	title   string
}

func parseAction(text string) action {
	trimmed := strings.TrimSpace(text)
	lower := strings.ToLower(trimmed)
	switch {
	case trimmed == "/followup":
		return action{kind: "followup"}
	case strings.HasPrefix(trimmed, "/rename "):
		return action{kind: "rename", title: strings.TrimSpace(trimmed[len("/rename "):])}
	case strings.HasPrefix(trimmed, "/scenario "):
		fields := strings.Fields(trimmed)
		if len(fields) >= 3 {
			return action{kind: "scenario", path: fields[2], command: strings.Join(fields[3:], " ")}
		}
	case strings.HasPrefix(trimmed, "/simulate "):
		return action{kind: "simulate", content: strings.TrimSpace(trimmed[len("/simulate "):])}
	case strings.HasPrefix(trimmed, "/bash "):
		fields := strings.Fields(trimmed[len("/bash "):])
		if len(fields) > 0 {
			return action{kind: "run", command: fields[0], args: fields[1:]}
		}
	case strings.HasPrefix(lower, "/plan "):
		return action{kind: "plan", content: strings.TrimSpace(trimmed[len("/plan "):])}
	case strings.HasPrefix(lower, "/read "):
		return action{kind: "read", path: strings.TrimSpace(trimmed[len("/read "):])}
	case strings.HasPrefix(lower, "/write "):
		parts := strings.SplitN(strings.TrimSpace(trimmed[len("/write "):]), " ", 2)
		if len(parts) == 2 {
			return action{kind: "write", path: parts[0], content: parts[1]}
		}
	case strings.HasPrefix(lower, "plan:"):
		return action{kind: "plan", content: strings.TrimSpace(trimmed[len("plan:"):])}
	case strings.Contains(lower, "emit a plan"):
		return action{kind: "plan", content: "Inspect requirements\nApply change\nVerify result"}
	case strings.Contains(lower, "read ./readme.md") || strings.Contains(lower, "read readme.md"):
		return action{kind: "read", path: "README.md"}
	case strings.HasPrefix(lower, "read "):
		path := strings.TrimSpace(trimmed[len("read "):])
		if before, _, ok := strings.Cut(path, " and "); ok {
			path = before
		}
		return action{kind: "read", path: path}
	case strings.HasPrefix(lower, "write the exact text"):
		return action{kind: "describe", content: trimmed}
	case strings.HasPrefix(lower, "write "):
		rest := strings.TrimSpace(trimmed[len("write "):])
		parts := strings.SplitN(rest, " ", 2)
		if len(parts) == 2 {
			return action{kind: "write", path: parts[0], content: parts[1]}
		}
	case strings.Contains(lower, "create ./.tmp/tmp-output.txt"):
		return action{kind: "write", path: ".tmp/tmp-output.txt", content: "READY"}
	case strings.HasPrefix(lower, "run ") && !strings.Contains(lower, "run `"):
		fields := strings.Fields(trimmed[len("run "):])
		if len(fields) > 0 {
			return action{kind: "run", command: fields[0], args: fields[1:]}
		}
	case strings.Contains(lower, "run `"):
		commandText := between(trimmed, "Run `", "`")
		if commandText == "" {
			commandText = between(trimmed, "run `", "`")
		}
		fields := strings.Fields(commandText)
		if len(fields) > 0 {
			return action{kind: "run", command: fields[0], args: fields[1:]}
		}
	case strings.HasPrefix(lower, "rename "):
		return action{kind: "rename", title: strings.TrimSpace(trimmed[len("rename "):])}
	}
	return action{kind: "describe", content: trimmed}
}

func (a *Agent) executeAction(ctx context.Context, session *Session, act action) error {
	switch act.kind {
	case "followup":
		if err := a.streamText(ctx, session.ID, "STARTED"); err != nil {
			return err
		}
		sessionID := session.ID
		go a.emitBackgroundFollowUp(sessionID)
		return nil
	case "plan":
		entries := []acp.PlanEntry{{Content: act.content, Status: "in_progress", Priority: "medium"}}
		_ = a.notify(ctx, session.ID, acp.SessionUpdate{SessionUpdate: "plan", Entries: entries})
		return a.streamText(ctx, session.ID, "Plan updated.")
	case "read", "write", "run":
		toolID := newID()
		kind := "read"
		title := "Read " + act.path
		if act.kind == "write" {
			kind = "edit"
			title = "Write " + act.path
		}
		if act.kind == "run" {
			kind = "execute"
			title = "Run " + act.command
		}
		path := resolvePath(session.CWD, act.path)
		raw, _ := json.Marshal(map[string]any{"path": path, "command": act.command, "args": act.args})
		status := "pending"
		if err := a.notify(ctx, session.ID, acp.SessionUpdate{SessionUpdate: "tool_call", ToolCallID: toolID, Title: &title, Kind: &kind, Status: &status, RawInput: raw}); err != nil {
			return err
		}
		if act.kind != "read" {
			allowed, err := a.permission(ctx, session, toolID, title, kind, raw)
			if err != nil {
				return err
			}
			if !allowed {
				status = "failed"
				_ = a.toolUpdate(ctx, session, acp.SessionUpdate{SessionUpdate: "tool_call_update", ToolCallID: toolID, Title: &title, Kind: &kind, Status: &status})
				return a.streamText(ctx, session.ID, "Permission denied.")
			}
		}
		status = "in_progress"
		if err := a.toolUpdate(ctx, session, acp.SessionUpdate{SessionUpdate: "tool_call_update", ToolCallID: toolID, Title: &title, Kind: &kind, Status: &status}); err != nil {
			return err
		}
		a.mu.Lock()
		caps := a.clientCapabilities
		a.mu.Unlock()
		var output string
		var err error
		switch act.kind {
		case "read":
			if caps.FS.ReadTextFile {
				var response struct {
					Content string `json:"content"`
				}
				err = a.peer.Call(ctx, "fs/read_text_file", map[string]any{"sessionId": session.ID, "path": path}, &response)
				output = response.Content
			} else {
				var data []byte
				data, err = os.ReadFile(path)
				output = string(data)
			}
		case "write":
			if caps.FS.WriteTextFile {
				err = a.peer.Call(ctx, "fs/write_text_file", map[string]any{"sessionId": session.ID, "path": path, "content": act.content}, nil)
			} else {
				err = os.MkdirAll(filepath.Dir(path), 0700)
				if err == nil {
					err = os.WriteFile(path, []byte(act.content), 0600)
				}
			}
			output = "Wrote " + act.path
		case "run":
			if !caps.Terminal {
				err = fmt.Errorf("host does not support terminal execution")
			} else {
				output, err = a.runViaTerminal(ctx, session.ID, session.CWD, act.command, act.args)
			}
		}
		status = "completed"
		if err != nil {
			status = "failed"
			output = err.Error()
		}
		rawOutput, _ := json.Marshal(output)
		if notifyErr := a.toolUpdate(ctx, session, acp.SessionUpdate{SessionUpdate: "tool_call_update", ToolCallID: toolID, Title: &title, Kind: &kind, Status: &status, RawInput: raw, RawOutput: rawOutput}); notifyErr != nil {
			return notifyErr
		}
		if err != nil {
			return err
		}
		return a.streamText(ctx, session.ID, output)
	case "rename":
		session.Title = act.title
		updated := now()
		session.UpdatedAt = updated
		_ = a.notify(ctx, session.ID, acp.SessionUpdate{SessionUpdate: "session_info_update", Title: &session.Title, SessionInfoUpdate: acp.SessionInfoUpdate{Title: &session.Title, UpdatedAt: &updated}})
		return a.streamText(ctx, session.ID, "Renamed session.")
	case "scenario":
		if err := a.executeAction(ctx, session, actionType("plan", "Inspect fixture and perform host operations")); err != nil {
			return err
		}
		if err := a.executeAction(ctx, session, action{kind: "write", path: act.path, content: "READY\n"}); err != nil {
			return err
		}
		if err := a.executeAction(ctx, session, action{kind: "read", path: act.path}); err != nil {
			return err
		}
		fields := strings.Fields(act.command)
		if len(fields) > 0 {
			if err := a.executeAction(ctx, session, action{kind: "run", command: fields[0], args: fields[1:]}); err != nil {
				return err
			}
		}
		return a.executeAction(ctx, session, actionType("plan", "Verified host operations"))
	case "simulate":
		session.PendingFaults = append(session.PendingFaults, act.content)
		return a.streamText(ctx, session.ID, "Fault scheduled.")
	default:
		return a.streamText(ctx, session.ID, "OK")
	}
}

func (a *Agent) emitBackgroundFollowUp(sessionID string) {
	time.Sleep(80 * time.Millisecond)
	ctx := context.Background()
	status := "completed"
	title := "sleep 60 && echo ORPHAN-BG-DONE-42"
	kind := "execute"
	_ = a.notify(ctx, sessionID, acp.SessionUpdate{
		SessionUpdate: "tool_call_update",
		ToolCallID:    "bg-sleep",
		Title:         &title,
		Kind:          &kind,
		Status:        &status,
	})
	for _, chunk := range []string{"ORPHAN", "-BG-", "DONE"} {
		_ = a.notify(ctx, sessionID, acp.SessionUpdate{
			SessionUpdate: "agent_message_chunk",
			Type:          "text",
			Text:          chunk,
			MessageID:     "msg_followup_1",
		})
	}
	_ = a.notify(ctx, sessionID, acp.SessionUpdate{SessionUpdate: "available_commands_update"})
}

func (a *Agent) streamText(ctx context.Context, sessionID string, text string) error {
	chunks := []string{text}
	if len(text) > 24 {
		chunks = []string{text[:len(text)/2], text[len(text)/2:]}
	}
	for _, chunk := range chunks {
		if err := a.notify(ctx, sessionID, acp.SessionUpdate{SessionUpdate: "agent_message_chunk", Type: "text", Text: chunk}); err != nil {
			return err
		}
	}
	return nil
}

func promptText(parts []acp.ContentBlock) string {
	var out []string
	for _, part := range parts {
		if part.Type == "text" {
			out = append(out, part.Text)
		}
	}
	return strings.Join(out, "\n")
}

func between(value, start, end string) string {
	startIndex := strings.Index(value, start)
	if startIndex < 0 {
		return ""
	}
	rest := value[startIndex+len(start):]
	endIndex := strings.Index(rest, end)
	if endIndex < 0 {
		return ""
	}
	return rest[:endIndex]
}

func defaultModes() acp.SessionModeState {
	return acp.SessionModeState{
		CurrentModeID: "accept-edits",
		AvailableModes: []acp.SessionMode{
			{ID: "deny", Name: "Deny", Description: "Allows planning and reads, but blocks edits and terminal execution."},
			{ID: "accept-edits", Name: "Accept Edits", Description: "Allows file edits but still gates terminal execution."},
			{ID: "yolo", Name: "YOLO", Description: "Allows file edits and terminal execution."},
		},
	}
}

func defaultConfigOptions() []acp.SessionConfigOption {
	categoryMode := "mode"
	categoryModel := "model"
	return []acp.SessionConfigOption{
		{Type: "select", ID: "model", Name: "Model", Category: &categoryModel, Value: "gpt", Options: []acp.SessionConfigChoice{{Value: "gpt", Name: "GPT"}, {Value: "claude", Name: "Claude"}, {Value: "gemini", Name: "Gemini"}}},
		{Type: "select", ID: "reasoning_effort", Name: "Reasoning Effort", Category: &categoryMode, Value: "medium", Options: []acp.SessionConfigChoice{{Value: "low", Name: "Low"}, {Value: "medium", Name: "Medium"}, {Value: "high", Name: "High"}}},
	}
}

func resolvePath(cwd, value string) string {
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Join(cwd, value)
}

func now() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

func newID() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(bytes[:])
}

func invalidParams(message string) error {
	return &acp.RPCError{Code: -32602, Message: message}
}

func actionType(kind, content string) action { return action{kind: kind, content: content} }
func (a *Agent) toolUpdate(ctx context.Context, session *Session, update acp.SessionUpdate) error {
	if err := a.notify(ctx, session.ID, update); err != nil {
		return err
	}
	for i, fault := range session.PendingFaults {
		if fault == "duplicate-next-tool-update" {
			session.PendingFaults = append(session.PendingFaults[:i], session.PendingFaults[i+1:]...)
			return a.notify(ctx, session.ID, update)
		}
	}
	return nil
}
func (a *Agent) permission(ctx context.Context, session *Session, id, title, kind string, raw json.RawMessage) (bool, error) {
	if session.Modes.CurrentModeID == "deny" {
		return false, nil
	}
	if session.Modes.CurrentModeID == "yolo" {
		return true, nil
	}
	// Literal wire shape is independent from runtime permission DTOs.
	var response struct {
		Outcome struct {
			Outcome  string `json:"outcome"`
			OptionID string `json:"optionId"`
		} `json:"outcome"`
	}
	err := a.peer.Call(ctx, "session/request_permission", map[string]any{"sessionId": session.ID, "toolCall": map[string]any{"toolCallId": id, "title": title, "kind": kind, "rawInput": raw}, "options": []map[string]string{{"optionId": "allow-once", "name": "Allow once", "kind": "allow_once"}, {"optionId": "reject-once", "name": "Reject once", "kind": "reject_once"}}}, &response)
	if err != nil {
		return false, err
	}
	return response.Outcome.Outcome == "selected" && response.Outcome.OptionID == "allow-once", nil
}
func defaultCommands() []acp.AvailableCommand {
	return []acp.AvailableCommand{{Name: "help", Description: "List commands"}, {Name: "read", Description: "Read a file"}, {Name: "write", Description: "Write a file"}, {Name: "bash", Description: "Execute a command"}, {Name: "plan", Description: "Publish a plan"}, {Name: "rename", Description: "Rename session"}}
}
