package acpruntime

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

func TestSessionRejectsOperationsAfterClose(t *testing.T) {
	driver := &testSessionDriver{}
	session := &Session{driver: driver}
	if err := session.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if got := session.Status(); got != "closed" {
		t.Fatalf("Status() = %q, want closed", got)
	}
	handle := session.StartTurn(context.Background(), RuntimePrompt{Text: "hello"})
	result := <-handle.Completion
	assertSessionClosedError(t, result.Err)
	if got := driver.startCount.Load(); got != 0 {
		t.Fatalf("driver StartTurn count = %d, want 0", got)
	}
	if ok, err := session.CancelTurn(context.Background(), "turn-1"); ok || err == nil {
		t.Fatalf("CancelTurn() = %v, %v; want false, error", ok, err)
	} else {
		assertSessionClosedError(t, err)
	}
	assertSessionClosedError(t, session.SetAgentMode(context.Background(), "mode"))
	assertSessionClosedError(t, session.SetAgentConfigOption(context.Background(), "mode", "value"))
}

func TestSessionCloseIsConcurrentSafe(t *testing.T) {
	session := &Session{driver: &testSessionDriver{}}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = session.Status()
				_ = session.StartTurn(context.Background(), RuntimePrompt{Text: "hello"})
				_ = session.Close(context.Background())
			}
		}()
	}
	wg.Wait()
	if got := session.Status(); got != "closed" {
		t.Fatalf("Status() = %q, want closed", got)
	}
}

func TestNormalizeMCPServersEncodesEmptyArray(t *testing.T) {
	req := NewSessionRequest{CWD: "/tmp/project", MCPServers: normalizeMCPServers(nil)}
	bytes, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if string(bytes) != `{"cwd":"/tmp/project","mcpServers":[]}` {
		t.Fatalf("NewSessionRequest JSON = %s, want empty mcpServers array", bytes)
	}
}

func TestSessionUpdateAcceptsSingleContentBlock(t *testing.T) {
	var notification SessionNotification
	raw := []byte(`{"sessionId":"s1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"OK"}}}`)
	if err := json.Unmarshal(raw, &notification); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got := sessionUpdateText(notification.Update); got != "OK" {
		t.Fatalf("sessionUpdateText() = %q, want OK", got)
	}
}

func TestSessionUpdateAcceptsContentBlockArray(t *testing.T) {
	var notification SessionNotification
	raw := []byte(`{"sessionId":"s1","update":{"sessionUpdate":"agent_message_chunk","content":[{"type":"text","text":"OK"}]}}`)
	if err := json.Unmarshal(raw, &notification); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got := sessionUpdateText(notification.Update); got != "OK" {
		t.Fatalf("sessionUpdateText() = %q, want OK", got)
	}
}

func assertSessionClosedError(t *testing.T, err error) {
	t.Helper()
	var runtimeErr *RuntimeError
	if !errors.As(err, &runtimeErr) {
		t.Fatalf("error = %v, want RuntimeError", err)
	}
	if runtimeErr.Kind != ErrorSessionClosed {
		t.Fatalf("RuntimeError.Kind = %q, want %q", runtimeErr.Kind, ErrorSessionClosed)
	}
}

type testSessionDriver struct {
	startCount atomic.Int32
}

func (d *testSessionDriver) Close(context.Context) error { return nil }

func (d *testSessionDriver) CancelTurn(context.Context, string) (bool, error) { return true, nil }

func (d *testSessionDriver) SetAgentMode(context.Context, string) error { return nil }

func (d *testSessionDriver) SetAgentConfigOption(context.Context, string, any) error { return nil }

func (d *testSessionDriver) StartTurn(context.Context, RuntimePrompt) TurnHandle {
	d.startCount.Add(1)
	events := make(chan TurnEvent)
	close(events)
	completion := make(chan TurnResult, 1)
	completion <- TurnResult{Completion: TurnCompletion{TurnID: "turn-1", OutputText: "ok"}}
	close(completion)
	return TurnHandle{TurnID: "turn-1", Events: events, Completion: completion}
}

func (d *testSessionDriver) Snapshot() RuntimeSnapshot {
	return RuntimeSnapshot{Session: RuntimeSnapshotSession{ID: "session-1"}}
}

func (d *testSessionDriver) Status() string { return "ready" }

func (d *testSessionDriver) Capabilities() RuntimeCapabilities { return RuntimeCapabilities{} }

func (d *testSessionDriver) Diagnostics() RuntimeDiagnostics { return RuntimeDiagnostics{} }

func (d *testSessionDriver) Metadata() RuntimeSessionMetadata { return RuntimeSessionMetadata{} }

func (d *testSessionDriver) ThreadEntries() []ThreadEntry { return nil }

func (d *testSessionDriver) ToolCalls() []ToolCallSnapshot { return nil }

func (d *testSessionDriver) Operations() []Operation { return nil }

func (d *testSessionDriver) PermissionRequests() []PermissionRequestSnapshot { return nil }
