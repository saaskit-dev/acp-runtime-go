package acpruntime

import (
	"context"
	"encoding/json"
	"os"
	"strings"
)

type Connection struct {
	peer          *Peer
	observability ObservabilityOptions
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
	Info         Implementation
	Capabilities ClientCapabilities
	Authority    AuthorityHandlers
}

func NewConnection(peer *Peer, client Client) *Connection {
	return NewConnectionWithObservability(peer, client, ObservabilityOptions{})
}

func NewConnectionWithObservability(peer *Peer, client Client, observability ObservabilityOptions) *Connection {
	conn := &Connection{peer: peer, observability: observability}
	if client.Authority.Permission != nil {
		peer.RegisterRequest("session/request_permission", func(ctx context.Context, raw json.RawMessage) (any, error) {
			var req struct {
				SessionID  string             `json:"sessionId"`
				ToolCallID string             `json:"toolCallId"`
				Title      string             `json:"title"`
				Kind       string             `json:"kind"`
				Options    []PermissionOption `json:"options"`
			}
			if err := json.Unmarshal(raw, &req); err != nil {
				return nil, err
			}
			decision, err := client.Authority.Permission(ctx, PermissionRequest{
				SessionID:  req.SessionID,
				ToolCallID: req.ToolCallID,
				Title:      req.Title,
				Kind:       req.Kind,
				Options:    req.Options,
			})
			if err != nil {
				return nil, err
			}
			return permissionResponse{Outcome: decision.Outcome, OptionID: decision.OptionID}, nil
		})
	}
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
	return conn
}

func (c *Connection) SetSessionUpdateHandler(handler func(context.Context, SessionNotification)) {
	c.peer.RegisterNotification("session/update", func(ctx context.Context, raw json.RawMessage) {
		var notification SessionNotification
		if err := json.Unmarshal(raw, &notification); err != nil {
			c.emitProtocolError(ctx, "session/update", raw, err)
			return
		}
		handler(ctx, notification)
	})
}

func (c *Connection) Initialize(ctx context.Context, req InitializeRequest) (InitializeResponse, error) {
	var resp InitializeResponse
	err := c.peer.Call(ctx, "initialize", req, &resp)
	return resp, err
}

func (c *Connection) Authenticate(ctx context.Context, req AuthenticateRequest) (AuthenticateResponse, error) {
	var resp AuthenticateResponse
	err := c.peer.Call(ctx, "authenticate", req, &resp)
	return resp, err
}

func (c *Connection) NewSession(ctx context.Context, req NewSessionRequest) (NewSessionResponse, error) {
	var resp NewSessionResponse
	err := c.peer.Call(ctx, "session/new", req, &resp)
	return resp, err
}

func (c *Connection) LoadSession(ctx context.Context, req LoadSessionRequest) (LoadSessionResponse, error) {
	var resp LoadSessionResponse
	err := c.peer.Call(ctx, "session/load", req, &resp)
	return resp, err
}

func (c *Connection) ResumeSession(ctx context.Context, req ResumeSessionRequest) (ResumeSessionResponse, error) {
	var resp ResumeSessionResponse
	err := c.peer.Call(ctx, "session/resume", req, &resp)
	return resp, err
}

func (c *Connection) ForkSession(ctx context.Context, req ForkSessionRequest) (ForkSessionResponse, error) {
	var resp ForkSessionResponse
	err := c.peer.Call(ctx, "session/fork", req, &resp)
	return resp, err
}

func (c *Connection) ListSessions(ctx context.Context, req ListSessionsRequest) (ListSessionsResponse, error) {
	var resp ListSessionsResponse
	err := c.peer.Call(ctx, "session/list", req, &resp)
	return resp, err
}

func (c *Connection) Prompt(ctx context.Context, req PromptRequest) (PromptResponse, error) {
	var resp PromptResponse
	err := c.peer.Call(ctx, "session/prompt", req, &resp)
	return resp, err
}

func (c *Connection) Cancel(ctx context.Context, req CancelRequest) error {
	return c.peer.Notify(ctx, "session/cancel", req)
}

func (c *Connection) SetSessionMode(ctx context.Context, req SetSessionModeRequest) error {
	var resp SetSessionModeResponse
	return c.peer.Call(ctx, "session/set_mode", req, &resp)
}

func (c *Connection) SetSessionConfigOption(ctx context.Context, req SetSessionConfigOptionRequest) error {
	var resp SetSessionConfigOptionResponse
	return c.peer.Call(ctx, "session/set_config_option", req, &resp)
}

func (c *Connection) CloseSession(ctx context.Context, req CloseSessionRequest) error {
	var resp CloseSessionResponse
	return c.peer.Call(ctx, "session/close", req, &resp)
}

func defaultClient(options RuntimeOptions, handlers AuthorityHandlers) Client {
	info := options.ClientInfo
	if info.Name == "" {
		info = Implementation{Name: "acp-runtime-go", Version: "0.1.0"}
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
	return Client{
		Info: info,
		Capabilities: ClientCapabilities{
			Terminal: handlers.Terminal != nil,
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

type permissionResponse struct {
	Outcome  string `json:"outcome"`
	OptionID string `json:"optionId,omitempty"`
}

type readTextFileResponse struct {
	Content string `json:"content"`
}

type emptyResponse struct{}

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
