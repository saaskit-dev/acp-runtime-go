package acpruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
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

func TestDefaultClientAuthTerminalReflectsHandler(t *testing.T) {
	withoutTerminal := defaultClient(RuntimeOptions{}, AuthorityHandlers{})
	if withoutTerminal.Capabilities.Auth.Terminal {
		t.Fatalf("Auth.Terminal = true, want false without terminal handler")
	}
	withTerminal := defaultClient(RuntimeOptions{}, AuthorityHandlers{Terminal: noopTerminalHandler{}})
	if !withTerminal.Capabilities.Auth.Terminal {
		t.Fatalf("Auth.Terminal = false, want true with terminal handler")
	}
}

type noopTerminalHandler struct{}

func (noopTerminalHandler) CreateTerminal(ctx Context, request TerminalStartRequest) (TerminalSnapshot, error) {
	return TerminalSnapshot{}, nil
}

func (noopTerminalHandler) Output(ctx Context, terminalID string) (TerminalSnapshot, error) {
	return TerminalSnapshot{}, nil
}

func (noopTerminalHandler) Wait(ctx Context, terminalID string) (TerminalSnapshot, error) {
	return TerminalSnapshot{}, nil
}

func (noopTerminalHandler) Kill(ctx Context, terminalID string) (TerminalSnapshot, error) {
	return TerminalSnapshot{}, nil
}

func (noopTerminalHandler) Release(ctx Context, terminalID string) (TerminalSnapshot, error) {
	return TerminalSnapshot{}, nil
}
