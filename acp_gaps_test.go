package acpruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

// syncBuffer is a mutex-guarded buffer for capturing JSON-RPC writes from a
// Peer whose handlers may run on other goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf.Bytes()...)
}

func handlePermissionRequest(t *testing.T, conn *Connection, peer *Peer, params string) rpcMessage {
	t.Helper()
	peer.handleRequest(context.Background(), rpcMessage{
		JSONRPC: "2.0",
		ID:      json.RawMessage("7"),
		Method:  "session/request_permission",
		Params:  json.RawMessage(params),
	})
	var msg rpcMessage
	if err := json.Unmarshal(peer.writer.(*syncBuffer).Bytes(), &msg); err != nil {
		t.Fatalf("no well-formed response written: %v", err)
	}
	return msg
}

// Without a host permission authority, a session/request_permission from the
// agent must still get a spec-valid DENY answer — never -32601 method not
// found, which leaves agent-side failure behavior undefined.
func TestPermissionRequestWithoutAuthoritySelectsRejectOption(t *testing.T) {
	w := &syncBuffer{}
	peer := NewPeer(bytes.NewReader(nil), w, PeerOptions{})
	_ = NewConnection(peer, Client{}) // Authority.Permission == nil

	msg := handlePermissionRequest(t, nil, peer, `{"sessionId":"s1","toolCallId":"tc1","title":"rm -rf","kind":"execute","options":[{"id":"allow","name":"Allow","kind":"allow_once"},{"id":"deny","name":"Deny","kind":"reject_once"}]}`)
	if msg.Error != nil {
		t.Fatalf("got JSON-RPC error %v, want deny decision", msg.Error)
	}
	var resp permissionResponse
	if err := json.Unmarshal(msg.Result, &resp); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if resp.Outcome != "selected" || resp.OptionID != "deny" {
		t.Fatalf("outcome=%q optionID=%q, want selected/deny (the reject_once option)", resp.Outcome, resp.OptionID)
	}
}

func TestPermissionRequestWithoutAuthorityCancelsWithoutRejectOption(t *testing.T) {
	w := &syncBuffer{}
	peer := NewPeer(bytes.NewReader(nil), w, PeerOptions{})
	_ = NewConnection(peer, Client{})

	msg := handlePermissionRequest(t, nil, peer, `{"sessionId":"s1","toolCallId":"tc1","title":"fetch","kind":"fetch","options":[{"id":"allow","name":"Allow","kind":"allow_once"}]}`)
	if msg.Error != nil {
		t.Fatalf("got JSON-RPC error %v, want cancel outcome", msg.Error)
	}
	var resp permissionResponse
	if err := json.Unmarshal(msg.Result, &resp); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if resp.Outcome != "cancelled" {
		t.Fatalf("outcome=%q, want cancelled when no reject option exists", resp.Outcome)
	}
}

// Regression guard: a registered authority still decides, unchanged.
func TestPermissionRequestWithAuthorityStillConsulted(t *testing.T) {
	w := &syncBuffer{}
	peer := NewPeer(bytes.NewReader(nil), w, PeerOptions{})
	var seen PermissionRequest
	conn := NewConnection(peer, Client{Authority: AuthorityHandlers{
		Permission: func(ctx Context, req PermissionRequest) (PermissionDecision, error) {
			seen = req
			return PermissionDecision{Outcome: "selected", OptionID: "allow"}, nil
		},
	}})
	_ = conn

	msg := handlePermissionRequest(t, conn, peer, `{"sessionId":"s1","toolCallId":"tc1","kind":"execute","options":[{"id":"allow","kind":"allow_once"},{"id":"deny","kind":"reject_once"}]}`)
	if msg.Error != nil {
		t.Fatalf("got JSON-RPC error %v", msg.Error)
	}
	var resp permissionResponse
	if err := json.Unmarshal(msg.Result, &resp); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if resp.Outcome != "selected" || resp.OptionID != "allow" {
		t.Fatalf("outcome=%q optionID=%q, want host decision selected/allow", resp.Outcome, resp.OptionID)
	}
	if seen.ToolCallID != "tc1" {
		t.Fatalf("authority saw toolCallID=%q, want tc1", seen.ToolCallID)
	}
}

// bootstrapProtocolVersion wires a SessionService whose factory returns a
// scripted agent over pipes answering initialize with the given version.
func bootstrapProtocolVersion(t *testing.T, version int) error {
	t.Helper()
	providerReader, runtimeWriter := io.Pipe()
	runtimeReader, providerWriter := io.Pipe()
	runtimePeer := NewPeer(runtimeReader, runtimeWriter, PeerOptions{})
	providerPeer := NewPeer(providerReader, providerWriter, PeerOptions{})
	providerPeer.RegisterRequest("initialize", func(context.Context, json.RawMessage) (any, error) {
		return InitializeResponse{ProtocolVersion: version}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = runtimePeer.Start(ctx) }()
	go func() { _ = providerPeer.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		runtimePeer.Close()
		providerPeer.Close()
		_ = providerReader.Close()
		_ = runtimeWriter.Close()
		_ = runtimeReader.Close()
		_ = providerWriter.Close()
	})
	service := NewSessionService(func(ctx context.Context, input ConnectionFactoryInput) (ConnectionHandle, error) {
		conn := NewConnectionWithObservability(runtimePeer, input.Client, input.Observability)
		return ConnectionHandle{Connection: conn, Dispose: func(context.Context) error { return nil }}, nil
	}, RuntimeOptions{})
	_, err := service.bootstrap(ctx, Agent{Type: "test", Command: "test"}, t.TempDir(), nil, AuthorityHandlers{}, defaultAgentProfile())
	return err
}

func TestBootstrapRejectsNewerProtocolVersion(t *testing.T) {
	err := bootstrapProtocolVersion(t, 2)
	if err == nil {
		t.Fatal("bootstrap succeeded against agent speaking protocol version 2, want refusal")
	}
	var runtimeErr *RuntimeError
	if !asRuntimeError(err, &runtimeErr) || runtimeErr.Kind != ErrorProtocol {
		t.Fatalf("err = %v, want RuntimeError with kind=protocol", err)
	}
}

func TestBootstrapAcceptsCurrentAndUnversioned(t *testing.T) {
	if err := bootstrapProtocolVersion(t, 1); err != nil {
		t.Fatalf("protocolVersion 1 rejected: %v", err)
	}
	if err := bootstrapProtocolVersion(t, 0); err != nil {
		t.Fatalf("absent protocolVersion (0) rejected: %v", err)
	}
}

func asRuntimeError(err error, target **RuntimeError) bool {
	rte, ok := err.(*RuntimeError)
	if ok {
		*target = rte
	}
	return ok
}

// guard: keep the test honest about turn timeouts never hanging forever.

// A pending Call must unblock with a wrapped io.ErrClosedPipe carrying the
// transport-level cause when the transport dies mid-call (process death).
func TestPendingCallSurfacesCloseReason(t *testing.T) {
	clientReader, serverWriter := io.Pipe()
	serverReader, clientWriter := io.Pipe()
	client := NewPeer(clientReader, clientWriter, PeerOptions{})
	server := NewPeer(serverReader, serverWriter, PeerOptions{})
	block := make(chan struct{})
	server.RegisterRequest("session/prompt", func(ctx context.Context, _ json.RawMessage) (any, error) {
		<-block
		return PromptResponse{StopReason: "end_turn"}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = client.Start(ctx) }()
	go func() { _ = server.Start(ctx) }()
	t.Cleanup(func() {
		close(block)
		cancel()
		client.Close()
		server.Close()
		_ = clientReader.Close()
		_ = clientWriter.Close()
		_ = serverReader.Close()
		_ = serverWriter.Close()
	})

	callErr := make(chan error, 1)
	go func() {
		err := client.Call(ctx, "session/prompt", PromptRequest{SessionID: "s", Prompt: mapPrompt(RuntimePrompt{Text: "hi"})}, &PromptResponse{})
		callErr <- err
	}()
	// Give both peers a moment to exchange the request, then kill the
	// transport under the client so its read loop fails with a real cause.
	deadline := time.After(5 * time.Second)
	time.Sleep(100 * time.Millisecond)
	_ = clientReader.Close() // client read loop now fails
	select {
	case err := <-callErr:
		if err == nil {
			t.Fatal("pending call succeeded after transport death")
		}
		if !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("err = %v, want wrapped io.ErrClosedPipe", err)
		}
	case <-deadline:
		t.Fatal("pending call hung after transport death")
	}
}
