package acpruntime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestStartTurnRejectsConcurrentTurn(t *testing.T) {
	driver := newTestDriverWithActiveTurn(t, 1)
	// First turn is already currentTurn; second must fail without overwriting.
	handle := driver.StartTurn(context.Background(), RuntimePrompt{Text: "second"})
	result := <-handle.Completion
	if result.Err == nil {
		t.Fatal("second StartTurn error = nil, want turn-already-running")
	}
	var runtimeErr *RuntimeError
	if !errors.As(result.Err, &runtimeErr) {
		t.Fatalf("second StartTurn error = %T, want *RuntimeError", result.Err)
	}
	if runtimeErr.Kind != ErrorTurnCoalesced {
		t.Fatalf("RuntimeError.Kind = %q, want %q", runtimeErr.Kind, ErrorTurnCoalesced)
	}
	// Original turn must still be current.
	driver.mu.RLock()
	active := driver.currentTurn
	driver.mu.RUnlock()
	if active == nil || active.id != "turn-1" {
		t.Fatalf("currentTurn overwritten: %#v", active)
	}
}

func TestEmitTurnEventDoesNotBlockWhenBufferFull(t *testing.T) {
	driver := &acpSessionDriver{
		status:    "running",
		toolCalls: map[string]ToolCallSnapshot{},
		operations: map[string]Operation{},
		profile:   defaultAgentProfile(),
	}
	active := &activeTurn{
		id:     "turn-1",
		events: make(chan TurnEvent, 1),
	}
	// Fill the only buffer slot.
	active.events <- TurnEvent{Type: "started", TurnID: "turn-1"}
	driver.currentTurn = active

	done := make(chan struct{})
	go func() {
		// Would deadlock if emitTurnEvent blocked on a full channel.
		driver.emitTurnEvent(active, TurnEvent{Type: "text", TurnID: "turn-1", Text: "x"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("emitTurnEvent blocked on full events channel")
	}
}

func TestFinishTurnDeliversCompletionWhenEventsBufferFull(t *testing.T) {
	driver := &acpSessionDriver{status: "running"}
	active := &activeTurn{
		id:         "turn-1",
		events:     make(chan TurnEvent, 1),
		completion: make(chan TurnResult, 1),
	}
	// Saturate events so a blocking send would hang finishTurn.
	active.events <- TurnEvent{Type: "text", TurnID: "turn-1", Text: "buffered"}
	driver.currentTurn = active

	done := make(chan struct{})
	go func() {
		driver.finishTurn(active, TurnCompletion{TurnID: "turn-1", OutputText: "ok"}, nil)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("finishTurn blocked while events buffer was full")
	}

	select {
	case result := <-active.completion:
		if result.Err != nil {
			t.Fatalf("completion err = %v", result.Err)
		}
		if result.Completion.OutputText != "ok" {
			t.Fatalf("completion text = %q, want ok", result.Completion.OutputText)
		}
	case <-time.After(time.Second):
		t.Fatal("completion was not delivered")
	}
}

func TestCloseFinishesInFlightTurn(t *testing.T) {
	// Peer writer is enough for best-effort session/cancel Notify; CloseSession
	// uses Call and is bounded by the short context below.
	peer := NewPeer(neverEOFReader{}, discardWriter{}, PeerOptions{})
	driver := &acpSessionDriver{
		connection:  NewConnection(peer, Client{}),
		sessionID:   "session-1",
		status:      "running",
		toolCalls:   map[string]ToolCallSnapshot{},
		operations:  map[string]Operation{},
		permissions: map[string]PermissionRequestSnapshot{},
		rawConfig:   map[string]any{},
		profile:     defaultAgentProfile(),
	}
	active := &activeTurn{
		id:         "turn-1",
		events:     make(chan TurnEvent, 4),
		completion: make(chan TurnResult, 1),
	}
	driver.currentTurn = active

	// Close terminalizes the in-flight turn before waiting on CloseSession RPC.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	closeDone := make(chan error, 1)
	go func() { closeDone <- driver.Close(ctx) }()

	select {
	case result := <-active.completion:
		if result.Err == nil {
			t.Fatal("completion err = nil, want session closed")
		}
		var runtimeErr *RuntimeError
		if !errors.As(result.Err, &runtimeErr) {
			t.Fatalf("completion err = %T, want *RuntimeError", result.Err)
		}
		if runtimeErr.Kind != ErrorSessionClosed {
			t.Fatalf("RuntimeError.Kind = %q, want %q", runtimeErr.Kind, ErrorSessionClosed)
		}
	case <-time.After(time.Second):
		t.Fatal("in-flight turn was not finished on Close")
	}
	select {
	case <-closeDone:
	case <-time.After(time.Second):
		t.Fatal("Close did not return after context timeout")
	}
	if got := driver.Status(); got != "closed" {
		t.Fatalf("Status() = %q, want closed", got)
	}
}

func TestHandleSessionUpdateDoesNotBlockOnFullEvents(t *testing.T) {
	driver := &acpSessionDriver{
		sessionID:  "session-1",
		status:     "running",
		toolCalls:  map[string]ToolCallSnapshot{},
		operations: map[string]Operation{},
		rawConfig:  map[string]any{},
		profile:    defaultAgentProfile(),
	}
	active := &activeTurn{
		id:     "turn-1",
		events: make(chan TurnEvent, 1),
	}
	active.events <- TurnEvent{Type: "started", TurnID: "turn-1"}
	driver.currentTurn = active

	done := make(chan struct{})
	go func() {
		// Flood intermediate updates; none may block the notification path.
		for i := 0; i < 128; i++ {
			driver.handleSessionUpdate(SessionNotification{
				SessionID: "session-1",
				Update: SessionUpdate{
					SessionUpdate: "agent_message_chunk",
					Text:          "x",
				},
			})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handleSessionUpdate blocked on full events channel")
	}
	// Read model still accumulates text despite event drops.
	if got := active.outputText.String(); got == "" {
		t.Fatal("outputText empty; read model should update even when events drop")
	}
}

func TestFinishTurnIsIdempotent(t *testing.T) {
	driver := &acpSessionDriver{status: "running"}
	active := &activeTurn{
		id:         "turn-1",
		events:     make(chan TurnEvent, 4),
		completion: make(chan TurnResult, 1),
	}
	driver.currentTurn = active

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		driver.finishTurn(active, TurnCompletion{TurnID: "turn-1"}, nil)
	}()
	go func() {
		defer wg.Done()
		driver.finishTurn(active, TurnCompletion{}, &RuntimeError{Kind: ErrorSessionClosed, Op: "session.close", Msg: "closed"})
	}()
	wg.Wait()

	// Exactly one completion value; channel is closed.
	result, ok := <-active.completion
	if !ok {
		t.Fatal("completion closed without a result")
	}
	if _, ok := <-active.completion; ok {
		t.Fatal("completion delivered more than once")
	}
	// Result may be either path depending on race; just ensure we got one.
	_ = result
}

func newTestDriverWithActiveTurn(t *testing.T, buffer int) *acpSessionDriver {
	t.Helper()
	if buffer <= 0 {
		buffer = 1
	}
	driver := &acpSessionDriver{
		sessionID:   "session-1",
		status:      "running",
		toolCalls:   map[string]ToolCallSnapshot{},
		operations:  map[string]Operation{},
		permissions: map[string]PermissionRequestSnapshot{},
		rawConfig:   map[string]any{},
		profile:     defaultAgentProfile(),
	}
	active := &activeTurn{
		id:         "turn-1",
		events:     make(chan TurnEvent, buffer),
		completion: make(chan TurnResult, 1),
	}
	driver.currentTurn = active
	driver.turnSeq = 1
	return driver
}

// neverEOFReader blocks reads forever so Peer.Start (if launched) does not exit
// on EOF during unit tests that only need a Connection shell.
type neverEOFReader struct{}

func (neverEOFReader) Read([]byte) (int, error) {
	select {}
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
