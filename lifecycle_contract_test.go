package acpruntime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type closeBlockingWriter struct {
	entered chan struct{}
	closed  chan struct{}
	once    sync.Once
	exited  chan struct{}
}

func (w *closeBlockingWriter) Write([]byte) (int, error) {
	close(w.entered)
	<-w.closed
	close(w.exited)
	return 0, io.ErrClosedPipe
}
func (w *closeBlockingWriter) Close() error { w.once.Do(func() { close(w.closed) }); return nil }

func TestWriteDeadlineClosesTransportAndReleasesWriter(t *testing.T) {
	w := &closeBlockingWriter{entered: make(chan struct{}), closed: make(chan struct{}), exited: make(chan struct{})}
	p := NewPeer(strings.NewReader(""), w, PeerOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := p.Call(ctx, "blocked", nil, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error %v", err)
	}
	select {
	case <-w.exited:
	case <-time.After(time.Second):
		t.Fatal("writer not interrupted")
	}
	p.pendingMu.Lock()
	n := len(p.pending)
	p.pendingMu.Unlock()
	if n != 0 {
		t.Fatalf("pending=%d", n)
	}
	if err := p.Notify(context.Background(), "next", nil); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("tainted writer reused: %v", err)
	}
}

type shortFrameWriter struct{}

func (shortFrameWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }
func TestShortWriteNeverObservedAsSuccessful(t *testing.T) {
	var success atomic.Int32
	p := NewPeer(strings.NewReader(""), shortFrameWriter{}, PeerOptions{OnRawMessage: func(direction string, _ json.RawMessage) {
		if direction == "outbound" {
			success.Add(1)
		}
	}})
	err := p.Notify(context.Background(), "test", nil)
	if !errors.Is(err, io.ErrShortWrite) && !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("short write: %v", err)
	}
	if success.Load() != 0 {
		t.Fatal("short write counted as successful frame")
	}
}

func TestPromptTerminalAwaitsPriorReverseRequest(t *testing.T) {
	providerRead, hostWrite := io.Pipe()
	hostRead, providerWrite := io.Pipe()
	host := NewPeer(hostRead, hostWrite, PeerOptions{})
	defer host.Close()
	provider := NewPeer(providerRead, providerWrite, PeerOptions{})
	defer provider.Close()
	entered := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	host.RegisterRequest("permission", func(ctx context.Context, _ json.RawMessage) (any, error) {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return map[string]any{}, nil
	})
	provider.RegisterRequest("session/prompt", func(context.Context, json.RawMessage) (any, error) {
		if err := provider.writeMessage(rpcMessage{JSONRPC: "2.0", ID: json.RawMessage(`"p1"`), Method: "permission", Params: json.RawMessage(`{}`)}); err != nil {
			return nil, err
		}
		<-entered
		return map[string]any{}, nil
	})
	go host.Start(context.Background())
	go provider.Start(context.Background())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- host.Call(ctx, "session/prompt", nil, nil) }()
	<-entered
	select {
	case err := <-done:
		t.Fatalf("terminal bypassed earlier request: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancellation blocked behind reverse request")
	}
}

func TestSnapshotDeepOwnershipAndNumbers(t *testing.T) {
	type customInt int64
	d := &acpSessionDriver{agent: Agent{Env: map[string]string{"A": "one"}, Args: []string{"a"}}, rawConfig: map[string]any{"nested": map[string]any{"slice": []any{customInt(3)}}}, metadata: RuntimeSessionMetadata{AgentConfigOptions: []RuntimeAgentConfigOption{{ID: "x", Value: map[string]any{"v": []string{"original"}}}}}}
	snap := d.Snapshot()
	snap.Agent.Env["A"] = "two"
	snap.Agent.Args[0] = "b"
	snap.RawConfig["nested"].(map[string]any)["slice"].([]any)[0] = 4
	meta := d.Metadata()
	meta.AgentConfigOptions[0].Value.(map[string]any)["v"].([]string)[0] = "mutated"
	again := d.Snapshot()
	if again.Agent.Env["A"] != "one" || again.Agent.Args[0] != "a" {
		t.Fatal("agent aliases")
	}
	if _, ok := again.RawConfig["nested"].(map[string]any)["slice"].([]any)[0].(customInt); !ok {
		t.Fatal("typed number changed")
	}
	if d.Metadata().AgentConfigOptions[0].Value.(map[string]any)["v"].([]string)[0] != "original" {
		t.Fatal("metadata aliases")
	}
}

func TestBlockedHookDoesNotBlockTerminalDelivery(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	d := newTestDriverWithActiveTurn(t, 1)
	a := d.currentTurn
	d.hooks.OnEventDrop = func(RuntimeEventDrop) { close(entered); <-release }
	a.events <- TurnEvent{Type: "fill"}
	d.emitTurnEvent(a, TurnEvent{Type: "text"})
	<-entered
	d.finishTurn(a, TurnCompletion{OutputText: "authoritative"}, nil)
	select {
	case result := <-a.completion:
		if result.Completion.OutputText != "authoritative" {
			t.Fatal("lost completion")
		}
	case <-time.After(time.Second):
		t.Fatal("hook blocked completion")
	}
}

func TestLoadReplaysHistoryBeforeResponseWithRequestedIdentity(t *testing.T) {
	factory := reviewPipeFactory(t, func(p *Peer) {
		p.RegisterRequest("session/load", func(ctx context.Context, _ json.RawMessage) (any, error) {
			for _, update := range []string{
				`{"sessionId":"existing","update":{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"question"}}}`,
				`{"sessionId":"existing","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"ans"}}}`,
				`{"sessionId":"existing","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"wer"}}}`,
			} {
				if err := p.NotifyRaw(ctx, "session/update", json.RawMessage(update)); err != nil {
					return nil, err
				}
			}
			return json.RawMessage(`{}`), nil
		})
	})
	r := NewRuntime(factory, RuntimeOptions{})
	defer r.Close(context.Background())
	session, err := r.LoadSession(context.Background(), LoadSessionOptions{SessionID: "existing", StartSessionOptions: StartSessionOptions{Agent: Agent{Type: "fixture", Command: "unused"}}})
	if err != nil {
		t.Fatal(err)
	}
	entries := session.ThreadEntries()
	if session.Snapshot().Session.ID != "existing" || len(entries) != 2 || entries[0].Text != "question" || entries[1].Text != "answer" {
		t.Fatalf("lost replay: %+v", entries)
	}
}

func TestStartOptionsPreserveLiveAuthorityIdentity(t *testing.T) {
	authority := &recordingTerminalHandlerForRuntime{}
	original := StartSessionOptions{Agent: Agent{Env: map[string]string{"a": "one"}}, Handlers: AuthorityHandlers{Terminal: authority}, Meta: map[string]any{"a": []string{"one"}}}
	copied := cloneStartOptions(original)
	if copied.Handlers.Terminal != authority {
		t.Fatal("live handler was cloned")
	}
	copied.Agent.Env["a"] = "two"
	copied.Meta["a"].([]string)[0] = "two"
	if original.Agent.Env["a"] != "one" || original.Meta["a"].([]string)[0] != "one" {
		t.Fatal("config ownership was not detached")
	}
}

type retryCleanupDriver struct {
	testSessionDriver
	calls atomic.Int32
}

func (d *retryCleanupDriver) Close(context.Context) error {
	if d.calls.Add(1) == 1 {
		return errors.New("retry cleanup")
	}
	return nil
}
func TestRuntimeRetainsFailedCleanupOwnership(t *testing.T) {
	r := NewRuntime(nil, RuntimeOptions{})
	d := &retryCleanupDriver{}
	session := r.register(d)
	if err := session.Close(context.Background()); err == nil {
		t.Fatal("failed cleanup hidden")
	}
	if err := r.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if d.calls.Load() != 2 {
		t.Fatalf("cleanup ownership lost: calls=%d", d.calls.Load())
	}
	if err := r.Close(context.Background()); err != nil || d.calls.Load() != 2 {
		t.Fatal("already released driver closed again")
	}
}

func TestPromptImmediateCancelFrameIsRegisteredBeforeDispatch(t *testing.T) {
	const frames = `{"jsonrpc":"2.0","id":"work","method":"work","params":{}}` + "\n" + `{"jsonrpc":"2.0","method":"$/cancel_request","params":{"requestId":"work"}}` + "\n"
	for i := 0; i < 200; i++ {
		p := NewPeer(strings.NewReader(frames), io.Discard, PeerOptions{})
		done := make(chan bool, 1)
		p.RegisterRequest("work", func(ctx context.Context, _ json.RawMessage) (any, error) {
			<-ctx.Done()
			done <- true
			return nil, ctx.Err()
		})
		if err := p.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("immediately-following cancellation was lost")
		}
	}
}

func TestCancelWatchdogQuarantinesInsteadOfReusingStream(t *testing.T) {
	started := make(chan struct{})
	released := make(chan struct{})
	factory := reviewPipeFactory(t, func(p *Peer) {
		p.RegisterRequest("session/new", func(context.Context, json.RawMessage) (any, error) { return NewSessionResponse{SessionID: "s1"}, nil })
		p.RegisterRequest("session/prompt", func(context.Context, json.RawMessage) (any, error) {
			close(started)
			<-released
			return PromptResponse{StopReason: "end_turn"}, nil
		})
	})
	defer close(released)
	r := NewRuntime(factory, RuntimeOptions{})
	defer r.Close(context.Background())
	session, err := r.StartSession(context.Background(), StartSessionOptions{Agent: Agent{Type: "fixture", Command: "unused"}})
	if err != nil {
		t.Fatal(err)
	}
	turn := session.StartTurn(context.Background(), RuntimePrompt{Text: "first"})
	<-started
	if ok, err := session.CancelTurn(context.Background(), turn.TurnID); !ok || err != nil {
		t.Fatalf("cancel %v %v", ok, err)
	}
	driver := session.driver.(*acpSessionDriver)
	driver.mu.Lock()
	driver.currentTurn.cancelTimer.Reset(time.Millisecond)
	driver.mu.Unlock()
	select {
	case result := <-turn.Completion:
		if result.Err == nil {
			t.Fatal("watchdog pretended remote success")
		}
	case <-time.After(time.Second):
		t.Fatal("watchdog did not settle")
	}
	if driver.Status() != "tainted" {
		t.Fatalf("status=%s", driver.Status())
	}
	if result := <-session.StartTurn(context.Background(), RuntimePrompt{Text: "next"}).Completion; result.Err == nil {
		t.Fatal("tainted stream reused")
	}
}

func TestDeepClonePreservesCyclesNilAndTypedMaps(t *testing.T) {
	original := map[string]any{"nil": []string(nil), "empty": []string{}, "numbers": map[int]int64{7: 9}}
	original["cycle"] = original
	copied := cloneOwned(original)
	copied["cycle"].(map[string]any)["change"] = true
	if _, ok := original["change"]; ok {
		t.Fatal("cyclic map still aliases original")
	}
	if copied["nil"].([]string) != nil || copied["empty"].([]string) == nil {
		t.Fatal("nil and empty collapsed")
	}
	if copied["numbers"].(map[int]int64)[7] != 9 {
		t.Fatal("typed map changed")
	}
}

func TestFreshConnectionPolicyClosesSuccessfulTurnHandle(t *testing.T) {
	factory := reviewPipeFactory(t, func(p *Peer) {
		p.RegisterRequest("session/new", func(context.Context, json.RawMessage) (any, error) {
			return NewSessionResponse{SessionID: "fresh"}, nil
		})
		p.RegisterRequest("session/prompt", func(context.Context, json.RawMessage) (any, error) {
			return PromptResponse{StopReason: "end_turn"}, nil
		})
	})
	r := NewRuntime(factory, RuntimeOptions{RequireFreshConnectionPerTurn: true})
	defer r.Close(context.Background())
	session, err := r.StartSession(context.Background(), StartSessionOptions{Agent: Agent{Type: "fixture", Command: "unused"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.Run(context.Background(), "first"); err != nil {
		t.Fatal(err)
	}
	if session.Status() != "closed" {
		t.Fatalf("single-use handle status=%s", session.Status())
	}
	if _, err := session.Run(context.Background(), "second"); err == nil {
		t.Fatal("single-use transport reused")
	}
}

func TestResponseIDPreservesJSONType(t *testing.T) {
	p := NewPeer(strings.NewReader(""), io.Discard, PeerOptions{})
	defer p.Close()
	ch := make(chan rpcMessage, 1)
	p.pending[1] = ch
	p.pendingMethods[1] = "echo"
	p.pendingLife[1] = make(chan struct{})
	p.resolvePending(rpcMessage{ID: json.RawMessage(`"1"`), Result: json.RawMessage(`"wrong"`)})
	select {
	case <-ch:
		t.Fatal("string response ID resolved numeric request")
	default:
	}
	p.resolvePending(rpcMessage{ID: json.RawMessage(`1`), Result: json.RawMessage(`"correct"`)})
	if got := <-ch; string(got.Result) != `"correct"` {
		t.Fatalf("result=%s", got.Result)
	}
}
