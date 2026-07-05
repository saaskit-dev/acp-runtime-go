package acpruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSessionUpdateDecodeErrorReportsProtocolError(t *testing.T) {
	var events []ProtocolErrorEvent
	peer := NewPeer(bytes.NewReader(nil), &bytes.Buffer{}, PeerOptions{})
	conn := NewConnectionWithObservability(peer, Client{}, ObservabilityOptions{
		CaptureContent: "raw",
		OnProtocolError: func(ctx Context, event ProtocolErrorEvent) {
			events = append(events, event)
		},
	})
	var handled bool
	conn.SetSessionUpdateHandler(func(context.Context, SessionNotification) {
		handled = true
	})

	peer.handleNotification(context.Background(), rpcMessage{
		Method: "session/update",
		Params: json.RawMessage(`{"sessionId":`),
	})

	if handled {
		t.Fatalf("session update handler was called for invalid JSON")
	}
	if len(events) != 1 {
		t.Fatalf("protocol error count = %d, want 1", len(events))
	}
	if events[0].Method != "session/update" {
		t.Fatalf("Method = %q, want session/update", events[0].Method)
	}
	if events[0].Err == nil {
		t.Fatalf("Err is nil")
	}
	if string(events[0].Raw) != `{"sessionId":` {
		t.Fatalf("Raw = %q, want invalid session update payload", events[0].Raw)
	}
}

func TestProtocolErrorRawRequiresCaptureContent(t *testing.T) {
	var event ProtocolErrorEvent
	peer := NewPeer(bytes.NewReader(nil), &bytes.Buffer{}, PeerOptions{})
	conn := NewConnectionWithObservability(peer, Client{}, ObservabilityOptions{
		OnProtocolError: func(ctx Context, received ProtocolErrorEvent) {
			event = received
		},
	})
	conn.SetSessionUpdateHandler(func(context.Context, SessionNotification) {})

	peer.handleNotification(context.Background(), rpcMessage{
		Method: "session/update",
		Params: json.RawMessage(`{"sessionId":`),
	})

	if event.Err == nil {
		t.Fatalf("Err is nil")
	}
	if len(event.Raw) != 0 {
		t.Fatalf("Raw = %q, want empty without CaptureContent", event.Raw)
	}
}

func TestDefaultClientTerminalReflectsHandler(t *testing.T) {
	withoutTerminal := defaultClient(RuntimeOptions{}, AuthorityHandlers{})
	if withoutTerminal.Capabilities.Terminal {
		t.Fatalf("Terminal = true, want false without terminal handler")
	}
	withTerminal := defaultClient(RuntimeOptions{}, AuthorityHandlers{Terminal: noopTerminalHandler{}})
	if !withTerminal.Capabilities.Terminal {
		t.Fatalf("Terminal = false, want true with terminal handler")
	}
	encoded, err := json.Marshal(withTerminal.Capabilities)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if bytes.Contains(encoded, []byte(`"auth"`)) {
		t.Fatalf("ClientCapabilities JSON = %s, want no auth field", encoded)
	}
}

type noopTerminalHandler struct{}

func (noopTerminalHandler) CreateTerminal(ctx Context, request CreateTerminalRequest) (CreateTerminalResult, error) {
	return CreateTerminalResult{}, nil
}

func (noopTerminalHandler) Output(ctx Context, terminalID string) (TerminalOutputResult, error) {
	return TerminalOutputResult{}, nil
}

func (noopTerminalHandler) WaitForExit(ctx Context, terminalID string) (TerminalExitStatus, error) {
	return TerminalExitStatus{}, nil
}

func (noopTerminalHandler) Kill(ctx Context, terminalID string) error {
	return nil
}

func (noopTerminalHandler) Release(ctx Context, terminalID string) error {
	return nil
}

// recordingTerminalHandler is a test double that records calls and returns
// canned responses, letting the round-trip test assert that the host receives
// the wire params the agent sent and that the host's return value is encoded
// back on the wire exactly per the ACP v1 schema.
type recordingTerminalHandler struct {
	mu          sync.Mutex
	createReqs  []CreateTerminalRequest
	outputIDs   []string
	waitIDs     []string
	killIDs     []string
	releaseIDs  []string
	terminalID  string
	output      string
	exitCode    uint32
	exitSignal  string
}

func (h *recordingTerminalHandler) CreateTerminal(ctx Context, request CreateTerminalRequest) (CreateTerminalResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.createReqs = append(h.createReqs, request)
	id := h.terminalID
	if id == "" {
		id = "term-1"
	}
	return CreateTerminalResult{TerminalID: id}, nil
}

func (h *recordingTerminalHandler) Output(ctx Context, terminalID string) (TerminalOutputResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.outputIDs = append(h.outputIDs, terminalID)
	out := h.output
	if out == "" {
		out = "hello\n"
	}
	var status *TerminalExitStatus
	if h.exitCode != 0 || h.exitSignal != "" {
		status = h.exitStatus()
	}
	return TerminalOutputResult{Output: out, Truncated: false, ExitStatus: status}, nil
}

func (h *recordingTerminalHandler) WaitForExit(ctx Context, terminalID string) (TerminalExitStatus, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.waitIDs = append(h.waitIDs, terminalID)
	return *h.exitStatus(), nil
}

func (h *recordingTerminalHandler) Kill(ctx Context, terminalID string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.killIDs = append(h.killIDs, terminalID)
	return nil
}

func (h *recordingTerminalHandler) Release(ctx Context, terminalID string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.releaseIDs = append(h.releaseIDs, terminalID)
	return nil
}

func (h *recordingTerminalHandler) exitStatus() *TerminalExitStatus {
	status := &TerminalExitStatus{}
	if h.exitCode != 0 {
		code := h.exitCode
		status.ExitCode = &code
	}
	if h.exitSignal != "" {
		sig := h.exitSignal
		status.Signal = &sig
	}
	return status
}

// TestTerminalHandlersRoundTrip exercises all five ACP terminal methods over a
// real Peer pair, asserting both that the host sees the right params and that
// the agent receives the exact ACP v1 wire-format response.
func TestTerminalHandlersRoundTrip(t *testing.T) {
	handler := &recordingTerminalHandler{exitCode: 0}

	clientReader, serverWriter := io.Pipe()
	serverReader, clientWriter := io.Pipe()
	defer clientReader.Close()
	defer serverWriter.Close()
	defer serverReader.Close()
	defer clientWriter.Close()

	server := NewPeer(serverReader, serverWriter, PeerOptions{})
	serverConn := NewConnectionWithObservability(server, Client{
		Info:         Implementation{Name: "test", Version: "0"},
		Capabilities: ClientCapabilities{Terminal: true},
		Authority:    AuthorityHandlers{Terminal: handler},
	}, ObservabilityOptions{})
	_ = serverConn

	client := NewPeer(clientReader, clientWriter, PeerOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = server.Start(ctx) }()
	go func() { _ = client.Start(ctx) }()
	defer server.Close()
	defer client.Close()

	// terminal/create: assert host receives params and agent gets {terminalId}.
	var createResp struct {
		TerminalID string `json:"terminalId"`
	}
	if err := client.Call(ctx, "terminal/create", map[string]any{
		"sessionId": "s1",
		"command":   "echo",
		"args":      []string{"hi"},
		"env":       []map[string]string{{"name": "FOO", "value": "bar"}},
		"cwd":       "/tmp",
	}, &createResp); err != nil {
		t.Fatalf("terminal/create error = %v", err)
	}
	if createResp.TerminalID != "term-1" {
		t.Fatalf("terminalId = %q, want term-1", createResp.TerminalID)
	}
	if len(handler.createReqs) != 1 {
		t.Fatalf("createReqs = %d, want 1", len(handler.createReqs))
	}
	got := handler.createReqs[0]
	if got.Command != "echo" || len(got.Args) != 1 || got.Args[0] != "hi" || got.CWD != "/tmp" {
		t.Fatalf("CreateTerminalRequest = %+v", got)
	}
	if len(got.Env) != 1 || got.Env[0].Name != "FOO" || got.Env[0].Value != "bar" {
		t.Fatalf("Env = %+v, want [{FOO bar}]", got.Env)
	}

	// terminal/output: assert response is {output, truncated, exitStatus}.
	var outputResp struct {
		Output     string `json:"output"`
		Truncated  bool   `json:"truncated"`
		ExitStatus *struct {
			ExitCode *uint32 `json:"exitCode"`
			Signal   *string `json:"signal"`
		} `json:"exitStatus"`
	}
	if err := client.Call(ctx, "terminal/output", terminalIDRequest{SessionID: "s1", TerminalID: "term-1"}, &outputResp); err != nil {
		t.Fatalf("terminal/output error = %v", err)
	}
	if outputResp.Output != "hello\n" || outputResp.Truncated {
		t.Fatalf("output response = %+v", outputResp)
	}
	// exitCode 0 should be omitted (omitempty) since we never set ExitCode.
	if outputResp.ExitStatus != nil && outputResp.ExitStatus.ExitCode != nil {
		t.Fatalf("expected no exitCode for zero value, got %+v", outputResp.ExitStatus)
	}

	// terminal/wait_for_exit: assert exitCode/signal are INLINED (not nested).
	var waitResp struct {
		ExitCode *uint32 `json:"exitCode"`
		Signal   *string `json:"signal"`
	}
	if err := client.Call(ctx, "terminal/wait_for_exit", terminalIDRequest{SessionID: "s1", TerminalID: "term-1"}, &waitResp); err != nil {
		t.Fatalf("terminal/wait_for_exit error = %v", err)
	}
	if waitResp.ExitCode != nil || waitResp.Signal != nil {
		t.Fatalf("wait response = %+v, want both nil for clean exit 0", waitResp)
	}

	// terminal/kill: empty response {}.
	var killResp json.RawMessage
	if err := client.Call(ctx, "terminal/kill", terminalIDRequest{SessionID: "s1", TerminalID: "term-1"}, &killResp); err != nil {
		t.Fatalf("terminal/kill error = %v", err)
	}
	if strings.TrimSpace(string(killResp)) != "{}" {
		t.Fatalf("kill response = %s, want {}", killResp)
	}

	// terminal/release: empty response {}.
	var releaseResp json.RawMessage
	if err := client.Call(ctx, "terminal/release", terminalIDRequest{SessionID: "s1", TerminalID: "term-1"}, &releaseResp); err != nil {
		t.Fatalf("terminal/release error = %v", err)
	}
	if strings.TrimSpace(string(releaseResp)) != "{}" {
		t.Fatalf("release response = %s, want {}", releaseResp)
	}
	if len(handler.releaseIDs) != 1 || handler.releaseIDs[0] != "term-1" {
		t.Fatalf("releaseIDs = %v", handler.releaseIDs)
	}
}

// TestTerminalSignalExitStatus asserts the exitStatus encoding when a command
// is killed by signal (exitCode nil, signal present).
func TestTerminalSignalExitStatus(t *testing.T) {
	handler := &recordingTerminalHandler{exitSignal: "SIGTERM"}

	clientReader, serverWriter := io.Pipe()
	serverReader, clientWriter := io.Pipe()
	defer clientReader.Close()
	defer serverWriter.Close()
	defer serverReader.Close()
	defer clientWriter.Close()

	server := NewPeer(serverReader, serverWriter, PeerOptions{})
	NewConnectionWithObservability(server, Client{
		Authority: AuthorityHandlers{Terminal: handler},
	}, ObservabilityOptions{})
	client := NewPeer(clientReader, clientWriter, PeerOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = server.Start(ctx) }()
	go func() { _ = client.Start(ctx) }()
	defer server.Close()
	defer client.Close()

	var waitResp struct {
		ExitCode *uint32 `json:"exitCode"`
		Signal   *string `json:"signal"`
	}
	if err := client.Call(ctx, "terminal/wait_for_exit", terminalIDRequest{TerminalID: "t"}, &waitResp); err != nil {
		t.Fatalf("wait_for_exit error = %v", err)
	}
	if waitResp.ExitCode != nil {
		t.Fatalf("ExitCode = %v, want nil for signal kill", waitResp.ExitCode)
	}
	if waitResp.Signal == nil || *waitResp.Signal != "SIGTERM" {
		t.Fatalf("Signal = %v, want SIGTERM", waitResp.Signal)
	}

	var outputResp struct {
		ExitStatus *struct {
			Signal *string `json:"signal"`
		} `json:"exitStatus"`
	}
	if err := client.Call(ctx, "terminal/output", terminalIDRequest{TerminalID: "t"}, &outputResp); err != nil {
		t.Fatalf("output error = %v", err)
	}
	if outputResp.ExitStatus == nil || outputResp.ExitStatus.Signal == nil || *outputResp.ExitStatus.Signal != "SIGTERM" {
		t.Fatalf("output exitStatus.Signal = %v, want SIGTERM", outputResp.ExitStatus)
	}
}

// TestTerminalHandlerNotRegisteredWhenAbsent asserts that no terminal methods
// are registered when Authority.Terminal is nil, so an agent calling them gets
// the standard JSON-RPC method-not-found error rather than a nil-panic.
func TestTerminalHandlerNotRegisteredWhenAbsent(t *testing.T) {
	clientReader, serverWriter := io.Pipe()
	serverReader, clientWriter := io.Pipe()
	defer clientReader.Close()
	defer serverWriter.Close()
	defer serverReader.Close()
	defer clientWriter.Close()

	server := NewPeer(serverReader, serverWriter, PeerOptions{})
	NewConnectionWithObservability(server, Client{Authority: AuthorityHandlers{}}, ObservabilityOptions{})
	client := NewPeer(clientReader, clientWriter, PeerOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = server.Start(ctx) }()
	go func() { _ = client.Start(ctx) }()
	defer server.Close()
	defer client.Close()

	err := client.Call(ctx, "terminal/create", map[string]any{"command": "echo"}, nil)
	if err == nil {
		t.Fatalf("expected method-not-found error, got nil")
	}
	rpcErr, ok := err.(*RPCError)
	if !ok {
		t.Fatalf("error type = %T, want *RPCError", err)
	}
	if rpcErr.Code != -32601 {
		t.Fatalf("error code = %d, want -32601", rpcErr.Code)
	}
}

// allowAllPermission is a minimal PermissionHandler that always allows.
func allowAllPermission(_ Context, _ PermissionRequest) (PermissionDecision, error) {
	return PermissionDecision{Outcome: "allow"}, nil
}

// TestPermissionObserverRecordsDecision verifies that the Connection invokes
// the registered permission observer with both the incoming request and the
// host's decision, so the runtime read model can record it.
func TestPermissionObserverRecordsDecision(t *testing.T) {
	clientReader, serverWriter := io.Pipe()
	serverReader, clientWriter := io.Pipe()
	defer clientReader.Close()
	defer serverWriter.Close()
	defer serverReader.Close()
	defer clientWriter.Close()

	server := NewPeer(serverReader, serverWriter, PeerOptions{})
	conn := NewConnectionWithObservability(server, Client{
		Authority: AuthorityHandlers{Permission: allowAllPermission},
	}, ObservabilityOptions{})

	var observedReq PermissionRequest
	var observedDecision PermissionDecision
	conn.SetPermissionObserver(func(req PermissionRequest, decision PermissionDecision) {
		observedReq = req
		observedDecision = decision
	})

	client := NewPeer(clientReader, clientWriter, PeerOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = server.Start(ctx) }()
	go func() { _ = client.Start(ctx) }()
	defer server.Close()
	defer client.Close()

	var resp permissionResponse
	if err := client.Call(ctx, "session/request_permission", map[string]any{
		"sessionId":  "s1",
		"toolCallId": "tool-7",
		"title":      "Run pwd",
		"kind":       "execute",
		"options":    []map[string]string{{"id": "allow", "name": "Allow"}},
	}, &resp); err != nil {
		t.Fatalf("request_permission error = %v", err)
	}
	if resp.Outcome != "allow" {
		t.Fatalf("response outcome = %q, want allow", resp.Outcome)
	}
	if observedReq.ToolCallID != "tool-7" || observedReq.Kind != "execute" {
		t.Fatalf("observed request = %+v", observedReq)
	}
	if observedDecision.Outcome != "allow" {
		t.Fatalf("observed decision = %+v", observedDecision)
	}
}

// TestPermissionObserverNotSetStillWorks verifies the observer is optional —
// omitting SetPermissionObserver must not break permission handling.
func TestPermissionObserverNotSetStillWorks(t *testing.T) {
	clientReader, serverWriter := io.Pipe()
	serverReader, clientWriter := io.Pipe()
	defer clientReader.Close()
	defer serverWriter.Close()
	defer serverReader.Close()
	defer clientWriter.Close()

	server := NewPeer(serverReader, serverWriter, PeerOptions{})
	NewConnectionWithObservability(server, Client{
		Authority: AuthorityHandlers{Permission: allowAllPermission},
	}, ObservabilityOptions{})
	client := NewPeer(clientReader, clientWriter, PeerOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = server.Start(ctx) }()
	go func() { _ = client.Start(ctx) }()
	defer server.Close()
	defer client.Close()

	var resp permissionResponse
	err := client.Call(ctx, "session/request_permission", map[string]any{
		"sessionId":  "s1",
		"toolCallId": "tool-1",
		"title":      "x",
		"kind":       "read",
	}, &resp)
	if err != nil {
		t.Fatalf("request_permission error without observer = %v", err)
	}
	if resp.Outcome != "allow" {
		t.Fatalf("outcome = %q, want allow", resp.Outcome)
	}
}
