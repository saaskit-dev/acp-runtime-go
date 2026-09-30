package acpruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
)

type Connection struct {
	peer          *Peer
	observability ObservabilityOptions

	permissionObserverMu sync.RWMutex
	permissionObserver   func(PermissionRequest, PermissionDecision)
	permissionGuard      func(PermissionRequest) bool
	permissionLease      func(PermissionRequest) func() bool
	elicitationLease     func(ElicitationRequest) func() bool
	authoritySlots       chan struct{}
	authorityCancels     map[uint64]authorityCancellation
	nextAuthorityID      uint64
	client               Client
	stateMu              sync.RWMutex
	initialized          bool
	agentCapabilities    AgentCapabilities
	authMethods          map[string]string
	configTypes          map[string]map[string]string
	elicitation          *elicitationState
}

// SetPermissionObserver registers a callback invoked whenever the agent sends
// a session/request_permission request. The host's decision is passed through
// unchanged so the runtime can record the request in its read model without
// affecting the outcome returned to the agent.
// SetRawMessageObserver replaces the protocol observer for this connection.
// Message buffers are copied, and registration is safe against peer IO.
func (c *Connection) SetRawMessageObserver(observer func(string, json.RawMessage)) {
	c.peer.rawMu.Lock()
	defer c.peer.rawMu.Unlock()
	c.peer.opts.OnRawMessage = observer
}

func (c *Connection) SetPermissionObserver(handler func(PermissionRequest, PermissionDecision)) {
	c.permissionObserverMu.Lock()
	defer c.permissionObserverMu.Unlock()
	c.permissionObserver = handler
}

// SetPermissionGuard binds approval to the active session/turn lifecycle. It is
// checked both before and after the host handler; false always cancels.
func (c *Connection) SetPermissionGuard(guard func(PermissionRequest) bool) {
	c.permissionObserverMu.Lock()
	defer c.permissionObserverMu.Unlock()
	c.permissionGuard = guard
}
func (c *Connection) SetPermissionLease(lease func(PermissionRequest) func() bool) {
	c.permissionObserverMu.Lock()
	defer c.permissionObserverMu.Unlock()
	c.permissionLease = lease
}
func (c *Connection) permissionLeaseFor(req PermissionRequest) func() bool {
	c.permissionObserverMu.RLock()
	factory := c.permissionLease
	c.permissionObserverMu.RUnlock()
	if factory == nil {
		return func() bool { return true }
	}
	lease := factory(req)
	if lease == nil {
		return func() bool { return false }
	}
	return lease
}
func (c *Connection) permissionAllowed(req PermissionRequest) bool {
	c.permissionObserverMu.RLock()
	guard := c.permissionGuard
	c.permissionObserverMu.RUnlock()
	return guard == nil || guard(req)
}

type ConnectionHandle struct {
	Connection *Connection
	Dispose    func(context.Context) error
}

type ConnectionFactoryInput struct {
	Agent         Agent
	Client        Client
	CWD           string
	Observability ObservabilityOptions
	Authority     AuthorityHandlers
}

type ConnectionFactory func(context.Context, ConnectionFactoryInput) (ConnectionHandle, error)

type Client struct {
	runtimeManaged                bool
	EnableExperimentalFeatures    bool
	Info                          Implementation
	Capabilities                  ClientCapabilities
	Authority                     AuthorityHandlers
	TerminalAuthenticationHandler TerminalAuthenticationHandler
}

// AfterReadIdle runs fn once inbound JSON-RPC has no complete line left.
// Used to snapshot TurnCompletion.OutputText after draining buffered
// session/update notifications that followed session/prompt on the wire.
func (c *Connection) AfterReadIdle(fn func()) {
	if c == nil || c.peer == nil {
		if fn != nil {
			fn()
		}
		return
	}
	c.peer.AfterIdle(fn)
}

func NewConnection(peer *Peer, client Client) *Connection {
	return NewConnectionWithObservability(peer, client, ObservabilityOptions{})
}

func NewConnectionWithObservability(peer *Peer, client Client, observability ObservabilityOptions) *Connection {
	conn := &Connection{peer: peer, observability: observability, client: client, configTypes: make(map[string]map[string]string), authoritySlots: make(chan struct{}, maxConcurrentAuthorityCalls), authorityCancels: make(map[uint64]authorityCancellation)}
	if client.runtimeManaged {
		conn.installStartupLeases()
	}
	// session/request_permission is ALWAYS answered, even when the host did
	// not register a permission authority. The ACP spec requires the client to
	// respond; leaving it unregistered would surface as JSON-RPC -32601
	// "method not found", leaving the agent's failure behavior undefined.
	// Without an authority the runtime fails closed via
	// defaultDenyPermissionDecision.
	peer.RegisterRequest("session/request_permission", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var permissionReq PermissionRequest
		if err := json.Unmarshal(raw, &permissionReq); err != nil {
			return nil, &RPCError{Code: -32602, Message: err.Error()}
		}
		lease := conn.permissionLeaseFor(permissionReq)
		var decision PermissionDecision
		if client.Authority.Permission != nil && conn.permissionAllowed(permissionReq) && lease() {
			created, err := runAuthority(conn, ctx, permissionReq.SessionID, client.Authority.PermissionTimeout, func(callCtx Context) (PermissionDecision, error) {
				return client.Authority.Permission(callCtx, cloneOwned(permissionReq))
			})
			if err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					decision = PermissionDecision{Outcome: "cancelled"}
				} else {
					decision = defaultDenyPermissionDecision(permissionReq)
				}
			} else {
				decision = validatedPermissionDecision(permissionReq, created)
			}
		} else {
			decision = defaultDenyPermissionDecision(permissionReq)
		}
		if ctx.Err() != nil || !conn.permissionAllowed(permissionReq) || !lease() {
			decision = PermissionDecision{Outcome: "cancelled"}
		}
		conn.permissionObserverMu.RLock()
		observer := conn.permissionObserver
		conn.permissionObserverMu.RUnlock()
		if observer != nil {
			observer(cloneOwned(permissionReq), decision)
		}
		if ctx.Err() != nil || !conn.permissionAllowed(permissionReq) || !lease() {
			decision = PermissionDecision{Outcome: "cancelled"}
		}
		return decision, nil
	})
	if client.Authority.Filesystem != nil {
		peer.RegisterRequest("fs/read_text_file", func(ctx context.Context, raw json.RawMessage) (any, error) {
			var req struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(raw, &req); err != nil {
				return nil, err
			}
			text, err := client.Authority.Filesystem.ReadTextFile(ctx, req.Path)
			if err != nil {
				return nil, err
			}
			return readTextFileResponse{Content: text}, nil
		})
		peer.RegisterRequest("fs/write_text_file", func(ctx context.Context, raw json.RawMessage) (any, error) {
			var req struct {
				Path    string `json:"path"`
				Content string `json:"content"`
			}
			if err := json.Unmarshal(raw, &req); err != nil {
				return nil, err
			}
			return emptyResponse{}, client.Authority.Filesystem.WriteTextFile(ctx, req.Path, req.Content)
		})
	}
	if client.Authority.Terminal != nil {
		registerTerminalHandlers(peer, client.Authority.Terminal)
	}
	conn.registerElicitationHandlers(client.Authority.Elicitation)
	return conn
}

// registerTerminalHandlers wires the five ACP terminal methods
// (terminal/create, terminal/output, terminal/wait_for_exit, terminal/kill,
// terminal/release) to the host-supplied TerminalHandler. These methods are
// invoked by the agent and implemented by the host. See
// https://agentclientprotocol.com/protocol/v1/terminals for the wire format.
//
// Note the protocol asymmetry: terminal/output nests the exit status under
// "exitStatus", while terminal/wait_for_exit inlines exitCode/signal at the
// top level of the result object.
func registerTerminalHandlers(peer *Peer, terminal TerminalHandler) {
	peer.RegisterRequest("terminal/create", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var req struct {
			SessionID       string        `json:"sessionId"`
			Command         string        `json:"command"`
			Args            []string      `json:"args,omitempty"`
			Env             []EnvVariable `json:"env,omitempty"`
			CWD             *string       `json:"cwd,omitempty"`
			OutputByteLimit *uint64       `json:"outputByteLimit,omitempty"`
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, err
		}
		var cwd string
		if req.CWD != nil {
			cwd = *req.CWD
		}
		result, err := terminal.CreateTerminal(ctx, CreateTerminalRequest{
			SessionID:       req.SessionID,
			Command:         req.Command,
			Args:            req.Args,
			Env:             req.Env,
			CWD:             cwd,
			OutputByteLimit: req.OutputByteLimit,
		})
		if err != nil {
			return nil, err
		}
		return createTerminalResponse{TerminalID: result.TerminalID}, nil
	})
	peer.RegisterRequest("terminal/output", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var req terminalIDRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, err
		}
		result, err := terminal.Output(ctx, req.TerminalID)
		if err != nil {
			return nil, err
		}
		return terminalOutputResponse{Output: result.Output, Truncated: result.Truncated, ExitStatus: toWireExitStatus(result.ExitStatus)}, nil
	})
	peer.RegisterRequest("terminal/wait_for_exit", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var req terminalIDRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, err
		}
		status, err := terminal.WaitForExit(ctx, req.TerminalID)
		if err != nil {
			return nil, err
		}
		return waitForTerminalExitResponse{ExitCode: status.ExitCode, Signal: status.Signal}, nil
	})
	peer.RegisterRequest("terminal/kill", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var req terminalIDRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, err
		}
		if err := terminal.Kill(ctx, req.TerminalID); err != nil {
			return nil, err
		}
		return emptyResponse{}, nil
	})
	peer.RegisterRequest("terminal/release", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var req terminalIDRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, err
		}
		if err := terminal.Release(ctx, req.TerminalID); err != nil {
			return nil, err
		}
		return emptyResponse{}, nil
	})
}

type terminalIDRequest struct {
	SessionID  string `json:"sessionId"`
	TerminalID string `json:"terminalId"`
}

func toWireExitStatus(status *TerminalExitStatus) *terminalExitStatusJSON {
	if status == nil {
		return nil
	}
	return &terminalExitStatusJSON{ExitCode: status.ExitCode, Signal: status.Signal}
}

func (c *Connection) SetSessionUpdateHandler(handler func(context.Context, SessionNotification)) {
	c.peer.RegisterNotification("session/update", func(ctx context.Context, raw json.RawMessage) {
		var notification SessionNotification
		if err := json.Unmarshal(raw, &notification); err != nil {
			c.emitProtocolError(ctx, "session/update", raw, err)
			return
		}
		if notification.Update.SessionUpdate == "config_option_update" {
			c.rememberConfigOptions(notification.SessionID, notification.Update.ConfigOptions)
		}
		handler(ctx, notification)
	})
}

// defaultDenyPermissionDecision synthesizes a fail-closed decision for hosts
// that did not register a permission authority. Preference: an explicit
// reject_* option from the agent's own option list (Outcome "selected" with
// its optionId); otherwise the generic Outcome "cancelled", which tells the
// agent no option was chosen. Both are spec-valid responses, and both deny.
func defaultDenyPermissionDecision(req PermissionRequest) PermissionDecision {
	for _, option := range req.Options {
		if option.ID != "" && (option.Kind == "reject_once" || option.Kind == "reject_always") {
			return PermissionDecision{Outcome: "selected", OptionID: option.ID}
		}
	}
	return PermissionDecision{Outcome: "cancelled"}
}

func (c *Connection) Initialize(ctx context.Context, req InitializeRequest) (InitializeResponse, error) {
	req.ClientCapabilities.Elicitation = elicitationCapabilities(c.client.Authority.Elicitation)
	req.ClientCapabilities.Auth = nil
	if c.client.TerminalAuthenticationHandler != nil {
		req.ClientCapabilities.Auth = &AuthCapabilities{Terminal: true}
	}
	var resp InitializeResponse
	err := c.peer.Call(ctx, "initialize", req, &resp)
	if err == nil {
		c.stateMu.Lock()
		c.initialized = true
		c.agentCapabilities = resp.AgentCapabilities
		c.authMethods = make(map[string]string)
		for _, m := range resp.AuthMethods {
			c.authMethods[m.ID] = m.Type
		}
		c.stateMu.Unlock()
	}
	return resp, err
}

func (c *Connection) Authenticate(ctx context.Context, req AuthenticateRequest) (AuthenticateResponse, error) {
	var resp AuthenticateResponse
	c.stateMu.RLock()
	kind, known := c.authMethods[req.MethodID]
	initialized := c.initialized
	c.stateMu.RUnlock()
	if kind == "terminal" {
		return resp, fmt.Errorf("terminal authentication method cannot be sent to authenticate")
	}
	if initialized && !known {
		return resp, fmt.Errorf("authentication method was not advertised")
	}
	err := c.peer.Call(ctx, "authenticate", req, &resp)
	return resp, err
}

func (c *Connection) NewSession(ctx context.Context, req NewSessionRequest) (NewSessionResponse, error) {
	var resp NewSessionResponse
	err := c.peer.Call(ctx, "session/new", req, &resp)
	if err == nil && strings.TrimSpace(resp.SessionID) == "" {
		return resp, &RPCError{Code: -32603, Message: "session/new omitted required sessionId"}
	}
	if err == nil {
		if resp.ConfigOptions != nil {
			c.rememberConfigOptions(resp.SessionID, resp.ConfigOptions)
		}
	}
	return resp, err
}

func (c *Connection) LoadSession(ctx context.Context, req LoadSessionRequest) (LoadSessionResponse, error) {
	if err := c.requireCapability("session/load"); err != nil {
		return LoadSessionResponse{}, err
	}
	if req.SessionID == "" {
		return LoadSessionResponse{}, fmt.Errorf("session/load requires nonempty sessionId")
	}
	var wire existingSessionWireResponse
	if err := c.peer.Call(ctx, "session/load", req, &wire); err != nil {
		return LoadSessionResponse{}, err
	}
	if wire.ConfigOptions != nil {
		c.rememberConfigOptions(req.SessionID, wire.ConfigOptions)
	}
	return wire.normalized(req.SessionID)
}

func (c *Connection) ResumeSession(ctx context.Context, req ResumeSessionRequest) (ResumeSessionResponse, error) {
	if err := c.requireCapability("session/resume"); err != nil {
		return ResumeSessionResponse{}, err
	}
	if req.SessionID == "" {
		return ResumeSessionResponse{}, fmt.Errorf("session/resume requires nonempty sessionId")
	}
	var wire existingSessionWireResponse
	if err := c.peer.Call(ctx, "session/resume", req, &wire); err != nil {
		return ResumeSessionResponse{}, err
	}
	if wire.ConfigOptions != nil {
		c.rememberConfigOptions(req.SessionID, wire.ConfigOptions)
	}
	return wire.normalized(req.SessionID)
}

func (c *Connection) ForkSession(ctx context.Context, req ForkSessionRequest) (ForkSessionResponse, error) {
	if !c.client.EnableExperimentalFeatures {
		return ForkSessionResponse{}, fmt.Errorf("session/fork is experimental; enable explicitly")
	}
	var resp ForkSessionResponse
	err := c.peer.Call(ctx, "session/fork", req, &resp)
	return resp, err
}

func (c *Connection) ListSessions(ctx context.Context, req ListSessionsRequest) (ListSessionsResponse, error) {
	if err := c.requireCapability("session/list"); err != nil {
		return ListSessionsResponse{}, err
	}
	var resp ListSessionsResponse
	err := c.peer.Call(ctx, "session/list", req, &resp)
	return resp, err
}

func (c *Connection) Prompt(ctx context.Context, req PromptRequest) (PromptResponse, error) {
	return c.promptWithBoundary(ctx, req, nil)
}
func (c *Connection) promptWithBoundary(ctx context.Context, req PromptRequest, boundary func()) (PromptResponse, error) {
	var response PromptResponse
	params, err := json.Marshal(req)
	if err != nil {
		return response, err
	}
	raw, err := c.peer.callRawBoundary(ctx, "session/prompt", params, boundary)
	if err != nil {
		return response, err
	}
	if err = json.Unmarshal(raw, &response); err != nil {
		return response, err
	}
	return response, nil
}

func (c *Connection) Cancel(ctx context.Context, req CancelRequest) error {
	c.cancelSessionAuthorities(req.SessionID)
	return c.peer.Notify(ctx, "session/cancel", req)
}

func (c *Connection) SetSessionMode(ctx context.Context, req SetSessionModeRequest) error {
	var resp SetSessionModeResponse
	return c.peer.Call(ctx, "session/set_mode", req, &resp)
}

func (c *Connection) SetSessionConfigOption(ctx context.Context, req SetSessionConfigOptionRequest) (SetSessionConfigOptionResponse, error) {
	var resp SetSessionConfigOptionResponse
	if _, boolean := req.Value.(bool); boolean {
		c.stateMu.RLock()
		kind := c.configTypes[req.SessionID][req.OptionID]
		c.stateMu.RUnlock()
		if kind != "boolean" {
			return resp, fmt.Errorf("boolean config option was not advertised")
		}
	}
	err := c.peer.Call(ctx, "session/set_config_option", req, &resp)
	if err == nil && resp.ConfigOptions != nil {
		c.rememberConfigOptions(req.SessionID, *resp.ConfigOptions)
	}
	return resp, err
}

func (c *Connection) CloseSession(ctx context.Context, req CloseSessionRequest) error {
	if err := c.requireCapability("session/close"); err != nil {
		return err
	}
	var resp CloseSessionResponse
	return c.peer.Call(ctx, "session/close", req, &resp)
}

// DeleteSession deletes a session's persistent history (session/delete). Unlike
// CloseSession, this removes the session from the agent's storage entirely.
func (c *Connection) DeleteSession(ctx context.Context, req DeleteSessionRequest) error {
	if err := c.requireCapability("session/delete"); err != nil {
		return err
	}
	var resp DeleteSessionResponse
	return c.peer.Call(ctx, "session/delete", req, &resp)
}

// Logout asks the agent to discard cached credentials (logout).
func (c *Connection) Logout(ctx context.Context, req LogoutRequest) error {
	if err := c.requireCapability("logout"); err != nil {
		return err
	}
	var resp LogoutResponse
	return c.peer.Call(ctx, "logout", req, &resp)
}

func defaultClient(options RuntimeOptions, handlers AuthorityHandlers) Client {
	info := options.ClientInfo
	if info.Name == "" {
		info = Implementation{Name: "acp-runtime-go", Version: "0.1.0"}
	}
	if handlers.PermissionTimeout == 0 {
		handlers.PermissionTimeout = options.AuthorityHandlers.PermissionTimeout
	}
	if handlers.Permission == nil {
		handlers.Permission = options.AuthorityHandlers.Permission
	}
	if handlers.Filesystem == nil {
		handlers.Filesystem = options.AuthorityHandlers.Filesystem
	}
	if handlers.Terminal == nil {
		handlers.Terminal = options.AuthorityHandlers.Terminal
	}
	handlers.Elicitation = mergeElicitationHandlers(handlers.Elicitation, options.AuthorityHandlers.Elicitation)
	return Client{
		Info:                          info,
		TerminalAuthenticationHandler: options.TerminalAuthenticationHandler,
		EnableExperimentalFeatures:    options.EnableExperimentalFeatures,
		Capabilities: ClientCapabilities{
			Session:     &ClientSessionCapabilities{ConfigOptions: &SessionConfigOptionsCapabilities{Boolean: &EmptyCapability{}}},
			Auth:        terminalAuthenticationCapabilities(options.TerminalAuthenticationHandler),
			Elicitation: elicitationCapabilities(handlers.Elicitation),
			Terminal:    handlers.Terminal != nil,
			FS: FilesystemCapabilities{
				ReadTextFile:  handlers.Filesystem != nil,
				WriteTextFile: handlers.Filesystem != nil,
			},
		},
		Authority: handlers,
	}
}

func envSlice(env map[string]string) []string {
	if len(env) == 0 {
		return os.Environ()
	}
	merged := map[string]string{}
	for _, item := range os.Environ() {
		for i := 0; i < len(item); i++ {
			if item[i] == '=' {
				merged[item[:i]] = item[i+1:]
				break
			}
		}
	}
	for key, value := range env {
		merged[key] = value
	}
	out := make([]string, 0, len(merged))
	for key, value := range merged {
		out = append(out, key+"="+value)
	}
	return out
}

type readTextFileResponse struct {
	Content string `json:"content"`
}

type emptyResponse struct{}

type createTerminalResponse struct {
	TerminalID string `json:"terminalId"`
}

type terminalOutputResponse struct {
	Output     string                  `json:"output"`
	Truncated  bool                    `json:"truncated"`
	ExitStatus *terminalExitStatusJSON `json:"exitStatus,omitempty"`
}

// waitForTerminalExitResponse inlines exitCode/signal at the top level per
// the ACP v1 schema (asymmetric with terminal/output, which nests them).
type waitForTerminalExitResponse struct {
	ExitCode *uint32 `json:"exitCode,omitempty"`
	Signal   *string `json:"signal,omitempty"`
}

type terminalExitStatusJSON struct {
	ExitCode *uint32 `json:"exitCode,omitempty"`
	Signal   *string `json:"signal,omitempty"`
}

func (c *Connection) emitProtocolError(ctx context.Context, method string, raw json.RawMessage, err error) {
	if c.observability.OnProtocolError == nil {
		return
	}
	event := ProtocolErrorEvent{Method: method, Err: err}
	if shouldCaptureProtocolErrorRaw(c.observability.CaptureContent) {
		event.Raw = copyRawMessage(raw)
	}
	c.observability.OnProtocolError(ctx, event)
}

func shouldCaptureProtocolErrorRaw(mode string) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "all", "full", "raw":
		return true
	default:
		return false
	}
}

func (c *Connection) installStartupLeases() {
	c.SetPermissionLease(func(PermissionRequest) func() bool { return func() bool { return false } })
	c.SetElicitationLease(func(req ElicitationRequest) func() bool { return func() bool { return req.SessionID == "" } })
}
