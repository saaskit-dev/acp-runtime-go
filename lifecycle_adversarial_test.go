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

// These isolated regressions exercise public API paths with independent raw
// provider frames rather than asserting the implementation's internal state.
func TestAdversarialCloseInterruptsBlockedConfigurationCall(t *testing.T) {
	entered := make(chan struct{})
	factory := reviewPipeFactory(t, func(p *Peer) {
		p.RegisterRequest("session/new", func(context.Context, json.RawMessage) (any, error) {
			return json.RawMessage(`{"sessionId":"s"}`), nil
		})
		p.RegisterRequest("session/set_mode", func(ctx context.Context, _ json.RawMessage) (any, error) {
			close(entered)
			<-ctx.Done()
			return nil, ctx.Err()
		})
	})
	r := NewRuntime(factory, RuntimeOptions{})
	defer r.Close(context.Background())
	s, err := r.StartSession(context.Background(), StartSessionOptions{Agent: Agent{Type: "fixture", Command: "unused"}})
	if err != nil {
		t.Fatal(err)
	}
	modeCtx, cancelMode := context.WithCancel(context.Background())
	defer cancelMode()
	modeDone := make(chan error, 1)
	go func() { modeDone <- s.SetAgentMode(modeCtx, "blocked") }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("configuration request did not reach provider")
	}
	closeCtx, cancelClose := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancelClose()
	closeDone := make(chan error, 1)
	go func() { closeDone <- s.Close(closeCtx) }()
	blocked := false
	select {
	case <-closeDone:
	case <-time.After(200 * time.Millisecond):
		blocked = true
	}
	// Release the original RPC explicitly so this negative test never leaks a
	// goroutine or relies on the test runner's global timeout for cleanup.
	cancelMode()
	select {
	case <-modeDone:
	case <-time.After(time.Second):
		t.Fatal("configuration call did not cancel")
	}
	if blocked {
		select {
		case <-closeDone:
		case <-time.After(time.Second):
			t.Fatal("Close remained blocked after configuration cancellation")
		}
		t.Fatal("Close could not reach transport cleanup while SetAgentMode held Session's read lock, despite Close deadline")
	}
}

func TestAdversarialLoadPreservesReplayOnlyMetadata(t *testing.T) {
	factory := reviewPipeFactory(t, func(p *Peer) {
		p.RegisterRequest("session/load", func(ctx context.Context, _ json.RawMessage) (any, error) {
			for _, frame := range []string{
				`{"sessionId":"s","update":{"sessionUpdate":"current_mode_update","currentModeId":"review"}}`,
				`{"sessionId":"s","update":{"sessionUpdate":"config_option_update","configOptions":[{"type":"select","id":"model","name":"Model","currentValue":"m","options":[{"value":"m","name":"M"}]}]}}`,
				`{"sessionId":"s","update":{"sessionUpdate":"session_info_update","title":"Saved title"}}`,
				`{"sessionId":"s","update":{"sessionUpdate":"available_commands_update","availableCommands":[{"name":"help","description":"Help"}]}}`,
			} {
				if err := p.NotifyRaw(ctx, "session/update", json.RawMessage(frame)); err != nil {
					return nil, err
				}
			}
			return json.RawMessage(`{}`), nil
		})
	})
	r := NewRuntime(factory, RuntimeOptions{})
	defer r.Close(context.Background())
	s, err := r.LoadSession(context.Background(), LoadSessionOptions{SessionID: "s", StartSessionOptions: StartSessionOptions{Agent: Agent{Type: "fixture", Command: "unused"}}})
	if err != nil {
		t.Fatal(err)
	}
	meta := s.Metadata()
	if meta.CurrentModeID != "review" || meta.Title != "Saved title" || len(meta.AgentConfigOptions) != 1 || len(meta.AvailableCommands) != 1 {
		t.Fatalf("valid pre-response state was discarded despite absent response metadata: %+v", meta)
	}
}

func TestAdversarialInitialModelRejectsMissingAuthoritativeReadback(t *testing.T) {
	factory := reviewPipeFactory(t, func(p *Peer) {
		p.RegisterRequest("session/new", func(context.Context, json.RawMessage) (any, error) {
			return json.RawMessage(`{"sessionId":"s","configOptions":[{"type":"select","id":"model","name":"Model","currentValue":"old","options":[{"value":"old","name":"Old"},{"value":"new","name":"New"}]}]}`), nil
		})
		p.RegisterRequest("session/set_config_option", func(context.Context, json.RawMessage) (any, error) {
			return json.RawMessage(`{"configOptions":[]}`), nil
		})
	})
	r := NewRuntime(factory, RuntimeOptions{})
	defer r.Close(context.Background())
	s, err := r.StartSession(context.Background(), StartSessionOptions{Agent: Agent{Type: "fixture", Command: "unused"}, InitialConfig: InitialConfig{Model: "new"}})
	if err == nil {
		t.Fatalf("missing model in authoritative readback claimed applied: %+v", s.Metadata().ConfigApplication)
	}
}

func TestAdversarialNewSessionRejectsMissingIdentity(t *testing.T) {
	factory := reviewPipeFactory(t, func(p *Peer) {
		p.RegisterRequest("session/new", func(context.Context, json.RawMessage) (any, error) {
			return json.RawMessage(`{}`), nil
		})
	})
	r := NewRuntime(factory, RuntimeOptions{})
	defer r.Close(context.Background())
	s, err := r.StartSession(context.Background(), StartSessionOptions{Agent: Agent{Type: "fixture", Command: "unused"}})
	if err == nil {
		t.Fatalf("session/new missing required identity accepted: %+v", s.Snapshot().Session)
	}
}

func TestAdversarialOrphanUpdateDoesNotAliasReadModel(t *testing.T) {
	d := &acpSessionDriver{
		sessionID: "s", status: "ready", toolCalls: map[string]ToolCallSnapshot{},
		operations: map[string]Operation{}, rawConfig: map[string]any{},
		updates: make(chan SessionNotification, 1),
		profile: ResolveAgentProfile(Agent{Type: "fixture"}),
	}
	d.handleSessionUpdate(SessionNotification{SessionID: "s", Update: SessionUpdate{
		SessionUpdate: "tool_call", ToolCallID: "tool",
		Content:  ContentBlocks{{Type: "text", Text: "original"}},
		RawInput: json.RawMessage(`{"x":1}`),
	}})
	event := <-d.SessionUpdates()
	event.Update.Content[0].Text = "mutated"
	event.Update.RawInput[5] = '9'
	tools := d.ToolCalls()
	if len(tools) != 1 || tools[0].Content[0].Text != "original" || string(tools[0].RawInput) != `{"x":1}` {
		t.Fatalf("caller mutation of Updates changed owned read model: %+v", tools)
	}
}

type adversarialBlockedCleanupDriver struct {
	testSessionDriver
	entered chan struct{}
	release chan struct{}
}

func (d *adversarialBlockedCleanupDriver) Close(context.Context) error {
	close(d.entered)
	<-d.release
	return nil
}

func TestAdversarialSessionCleanupContentionHonorsDeadline(t *testing.T) {
	d := &adversarialBlockedCleanupDriver{entered: make(chan struct{}), release: make(chan struct{})}
	s := newSession(nil, d)
	firstDone := make(chan error, 1)
	go func() { firstDone <- s.Close(context.Background()) }()
	<-d.entered
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	secondDone := make(chan error, 1)
	go func() { secondDone <- s.Close(ctx) }()
	var secondErr error
	blocked := false
	select {
	case secondErr = <-secondDone:
	case <-time.After(200 * time.Millisecond):
		blocked = true
	}
	close(d.release)
	<-firstDone
	if blocked {
		<-secondDone
		t.Fatal("second Session.Close ignored deadline while cleanup owner was blocked")
	}
	if !errors.Is(secondErr, context.DeadlineExceeded) {
		t.Fatalf("second close error = %v, want deadline", secondErr)
	}
}

func TestAdversarialDriverCleanupContentionHonorsDeadline(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	d := &acpSessionDriver{status: "ready", dispose: func(context.Context) error {
		close(entered)
		<-release
		return nil
	}}
	firstDone := make(chan error, 1)
	go func() { firstDone <- d.Close(context.Background()) }()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	secondDone := make(chan error, 1)
	go func() { secondDone <- d.Close(ctx) }()
	var secondErr error
	blocked := false
	select {
	case secondErr = <-secondDone:
	case <-time.After(200 * time.Millisecond):
		blocked = true
	}
	close(release)
	<-firstDone
	if blocked {
		<-secondDone
		t.Fatal("second driver.Close ignored deadline while cleanup owner was blocked")
	}
	if !errors.Is(secondErr, context.DeadlineExceeded) {
		t.Fatalf("second close error = %v, want deadline", secondErr)
	}
}

func TestAdversarialStartupFailureRetainsCleanupOwnership(t *testing.T) {
	var cleanupCalls atomic.Int32
	cleanupFailure := errors.New("fixture cleanup failed")
	base := reviewPipeFactory(t, func(p *Peer) {
		p.RegisterRequest("session/new", func(context.Context, json.RawMessage) (any, error) {
			return nil, &RPCError{Code: -32000, Message: "fixture session creation failed"}
		})
	})
	factory := func(ctx context.Context, input ConnectionFactoryInput) (ConnectionHandle, error) {
		handle, err := base(ctx, input)
		if err != nil {
			return handle, err
		}
		dispose := handle.Dispose
		handle.Dispose = func(ctx context.Context) error {
			if cleanupCalls.Add(1) == 1 {
				return cleanupFailure
			}
			return dispose(ctx)
		}
		return handle, nil
	}
	r := NewRuntime(factory, RuntimeOptions{})
	_, err := r.StartSession(context.Background(), StartSessionOptions{Agent: Agent{Type: "fixture", Command: "unused"}})
	if err == nil {
		t.Fatal("expected provider creation failure")
	}
	var runtimeErr *RuntimeError
	if !errors.As(err, &runtimeErr) || runtimeErr.CleanupError == nil {
		t.Errorf("startup error lost failed cleanup diagnostic: %v", err)
	}
	if err := r.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if cleanupCalls.Load() != 2 {
		t.Errorf("Runtime.Close lost failed startup handle: dispose calls=%d, want 2", cleanupCalls.Load())
	}
}

func TestAdversarialLoadReplayBooleanRemainsAdvertised(t *testing.T) {
	factory := reviewPipeFactory(t, func(p *Peer) {
		p.RegisterRequest("session/load", func(ctx context.Context, _ json.RawMessage) (any, error) {
			if err := p.NotifyRaw(ctx, "session/update", json.RawMessage(`{"sessionId":"s","update":{"sessionUpdate":"config_option_update","configOptions":[{"type":"boolean","id":"fast","name":"Fast","currentValue":false}]}}`)); err != nil {
				return nil, err
			}
			return json.RawMessage(`{}`), nil
		})
		p.RegisterRequest("session/set_config_option", func(context.Context, json.RawMessage) (any, error) {
			return json.RawMessage(`{"configOptions":[{"type":"boolean","id":"fast","name":"Fast","currentValue":true}]}`), nil
		})
	})
	r := NewRuntime(factory, RuntimeOptions{})
	defer r.Close(context.Background())
	s, err := r.LoadSession(context.Background(), LoadSessionOptions{SessionID: "s", StartSessionOptions: StartSessionOptions{Agent: Agent{Type: "fixture", Command: "unused"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetAgentConfigOption(context.Background(), "fast", true); err != nil {
		t.Fatalf("replay-advertised boolean was forgotten by connection: %v", err)
	}
}

func TestAdversarialInitialConfigChecksFinalSnapshot(t *testing.T) {
	factory := reviewPipeFactory(t, func(p *Peer) {
		p.RegisterRequest("session/new", func(context.Context, json.RawMessage) (any, error) {
			return json.RawMessage(`{"sessionId":"s","configOptions":[{"type":"select","id":"model","name":"Model","currentValue":"old","options":[{"value":"old","name":"Old"},{"value":"new","name":"New"}]},{"type":"select","id":"effort","name":"Effort","currentValue":"low","options":[{"value":"low","name":"Low"},{"value":"high","name":"High"}]}]}`), nil
		})
		p.RegisterRequest("session/set_config_option", func(_ context.Context, raw json.RawMessage) (any, error) {
			var req struct {
				ConfigID string `json:"configId"`
			}
			if err := json.Unmarshal(raw, &req); err != nil {
				return nil, err
			}
			if req.ConfigID == "model" {
				return json.RawMessage(`{"configOptions":[{"type":"select","id":"model","name":"Model","currentValue":"new","options":[{"value":"old","name":"Old"},{"value":"new","name":"New"}]},{"type":"select","id":"effort","name":"Effort","currentValue":"low","options":[{"value":"low","name":"Low"},{"value":"high","name":"High"}]}]}`), nil
			}
			return json.RawMessage(`{"configOptions":[{"type":"select","id":"model","name":"Model","currentValue":"old","options":[{"value":"old","name":"Old"},{"value":"new","name":"New"}]},{"type":"select","id":"effort","name":"Effort","currentValue":"high","options":[{"value":"low","name":"Low"},{"value":"high","name":"High"}]}]}`), nil
		})
	})
	r := NewRuntime(factory, RuntimeOptions{})
	defer r.Close(context.Background())
	s, err := r.StartSession(context.Background(), StartSessionOptions{Agent: Agent{Type: "fixture", Command: "unused"}, InitialConfig: InitialConfig{Model: "new", Effort: "high"}})
	if err == nil {
		t.Fatalf("later option reset requested model but startup succeeded: %+v", s.Metadata().ConfigApplication)
	}
}

func TestAdversarialCancelRequestSemanticIdentityBeforeEOF(t *testing.T) {
	for _, requestID := range []string{`"work"`, `"\u0077ork"`} {
		t.Run(requestID, func(t *testing.T) {
			reader, writer := io.Pipe()
			defer writer.Close()
			peer := NewPeer(reader, io.Discard, PeerOptions{})
			defer peer.Close()
			cancelled := make(chan struct{})
			peer.RegisterRequest("work", func(ctx context.Context, _ json.RawMessage) (any, error) {
				<-ctx.Done()
				close(cancelled)
				return nil, ctx.Err()
			})
			go peer.Start(context.Background())
			frames := `{"jsonrpc":"2.0","id":` + requestID + `,"method":"work","params":{}}` + "\n" + `{"jsonrpc":"2.0","method":"$/cancel_request","params":{"requestId":"work"}}` + "\n"
			if _, err := io.WriteString(writer, frames); err != nil {
				t.Fatal(err)
			}
			select {
			case <-cancelled:
			case <-time.After(200 * time.Millisecond):
				peer.Close()
				<-cancelled
				t.Fatal("matching cancellation did not reach request before transport EOF/Close")
			}
		})
	}
}

func TestAdversarialStartupPermissionHasNoActiveTurn(t *testing.T) {
	var hostCalls atomic.Int32
	decision := make(chan PermissionDecision, 1)
	factory := reviewPipeFactory(t, func(p *Peer) {
		p.RegisterRequest("session/new", func(ctx context.Context, _ json.RawMessage) (any, error) {
			var got PermissionDecision
			err := p.Call(ctx, "session/request_permission", json.RawMessage(`{"sessionId":"s","toolCall":{"toolCallId":"startup-tool","title":"Unsolicited startup operation"},"options":[{"optionId":"allow","name":"Allow","kind":"allow_once"},{"optionId":"reject","name":"Reject","kind":"reject_once"}]}`), &got)
			if err != nil {
				return nil, err
			}
			decision <- got
			return json.RawMessage(`{"sessionId":"s"}`), nil
		})
	})
	r := NewRuntime(factory, RuntimeOptions{AuthorityHandlers: AuthorityHandlers{Permission: func(Context, PermissionRequest) (PermissionDecision, error) {
		hostCalls.Add(1)
		return PermissionDecision{Outcome: "selected", OptionID: "allow"}, nil
	}}})
	defer r.Close(context.Background())
	_, err := r.StartSession(context.Background(), StartSessionOptions{Agent: Agent{Type: "fixture", Command: "unused"}})
	if err != nil {
		t.Fatal(err)
	}
	got := <-decision
	if hostCalls.Load() != 0 || got.Outcome == "selected" && got.OptionID == "allow" {
		t.Fatalf("permission bypassed active-turn lifecycle before driver attachment: handler calls=%d, decision=%+v", hostCalls.Load(), got)
	}
}

func TestAdversarialReplayEntryIDsSurvivePruning(t *testing.T) {
	d := &acpSessionDriver{sessionID: "s", status: "ready", maxThread: 2}
	for i, kind := range []string{"user_message_chunk", "agent_message_chunk", "user_message_chunk", "agent_message_chunk"} {
		d.handleReplayUpdate(SessionNotification{SessionID: "s", Update: SessionUpdate{SessionUpdate: kind, Text: string(rune('a' + i))}})
	}
	entries := d.ThreadEntries()
	if len(entries) != 2 || entries[0].ID == entries[1].ID {
		t.Fatalf("replay pruning reused live entry identity: %+v", entries)
	}
}

func TestAdversarialConcurrentCancelCannotReopenClosingDriver(t *testing.T) {
	d := newTestDriverWithActiveTurn(t, 4)
	d.connection = NewConnection(NewPeer(strings.NewReader(""), io.Discard, PeerOptions{}), Client{})
	defer d.connection.peer.Close()
	active, _ := d.beginClose()
	// This is the exact valid interleaving after Close's beginClose step but
	// before finishInFlightTurn, with CancelTurn already admitted by Session.
	_, _ = d.CancelTurn(context.Background(), active.id)
	d.finishTurn(active, TurnCompletion{}, sessionClosedError("session.close"))
	if d.Status() != "closed" {
		t.Fatalf("cancel raced close and reopened driver: status=%q", d.Status())
	}
}

func TestAdversarialTerminalBoundaryPreservesBeforeAndSeparatesAfterWhileDraining(t *testing.T) {
	driver := newOrphanTestDriver()
	providerReader, hostWriter := io.Pipe()
	hostReader, providerWriter := io.Pipe()
	lateWriter := &writeAfterStopReason{w: providerWriter, extra: []byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"agent_message_chunk","text":"AFTER"}}}` + "\n")}
	host := NewPeer(hostReader, hostWriter, PeerOptions{})
	provider := NewPeer(providerReader, lateWriter, PeerOptions{})
	defer providerWriter.Close()
	defer host.Close()
	defer provider.Close()
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseRequest := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseRequest()
	host.RegisterRequest("review/drain", func(ctx context.Context, _ json.RawMessage) (any, error) {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return json.RawMessage(`{}`), nil
	})
	provider.RegisterRequest("session/prompt", func(ctx context.Context, _ json.RawMessage) (any, error) {
		if err := provider.NotifyRaw(ctx, "session/update", json.RawMessage(`{"sessionId":"session-1","update":{"sessionUpdate":"agent_message_chunk","text":"BEFORE"}}`)); err != nil {
			return nil, err
		}
		if err := provider.writeMessage(rpcMessage{JSONRPC: "2.0", ID: json.RawMessage(`"drain"`), Method: "review/drain", Params: json.RawMessage(`{}`)}); err != nil {
			return nil, err
		}
		<-entered
		return json.RawMessage(`{"stopReason":"end_turn"}`), nil
	})
	connection := NewConnection(host, Client{})
	connection.SetSessionUpdateHandler(func(_ context.Context, n SessionNotification) { driver.handleSessionUpdate(n) })
	driver.connection = connection
	go host.Start(context.Background())
	go provider.Start(context.Background())
	turn := driver.StartTurn(context.Background(), RuntimePrompt{Text: "first"})
	select {
	case update := <-driver.SessionUpdates():
		if sessionUpdateText(update.Update) != "AFTER" {
			t.Fatalf("wrong separated update: %+v", update)
		}
	case <-time.After(time.Second):
		t.Fatal("post-response update did not reach separate stream while reverse request drained")
	}
	select {
	case result := <-turn.Completion:
		t.Fatalf("prompt terminal skipped prior reverse request barrier: %+v", result)
	default:
	}
	if result := <-driver.StartTurn(context.Background(), RuntimePrompt{Text: "too soon"}).Completion; result.Err == nil {
		t.Fatal("next turn started before prior reverse request drained")
	}
	releaseRequest()
	select {
	case result := <-turn.Completion:
		if result.Err != nil || result.Completion.OutputText != "BEFORE" {
			t.Fatalf("wire terminal text boundary incorrect: %+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("turn did not complete after reverse request drained")
	}
	for event := range turn.Events {
		if event.Type == "text" && event.Text == "AFTER" {
			t.Fatal("post-response text leaked into prior turn event stream")
		}
	}
}

func TestAdversarialRetiredStreamCannotMutateFreshSameIDSession(t *testing.T) {
	for _, retire := range []string{"cancelled", "disposed"} {
		t.Run(retire, func(t *testing.T) {
			var connections atomic.Int32
			oldStarted := make(chan struct{})
			newStarted := make(chan struct{})
			releaseNew := make(chan struct{})
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(releaseNew) }) }
			defer release()
			var oldProvider *Peer
			factory := reviewPipeFactory(t, func(provider *Peer) {
				number := connections.Add(1)
				if number == 1 {
					oldProvider = provider
				}
				provider.RegisterRequest("session/new", func(context.Context, json.RawMessage) (any, error) {
					return json.RawMessage(`{"sessionId":"same-id"}`), nil
				})
				provider.RegisterRequest("session/prompt", func(ctx context.Context, _ json.RawMessage) (any, error) {
					if number == 1 {
						close(oldStarted)
						if retire == "cancelled" {
							<-ctx.Done()
							return nil, ctx.Err()
						}
						return json.RawMessage(`{"stopReason":"end_turn"}`), nil
					}
					close(newStarted)
					select {
					case <-releaseNew:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
					if err := provider.NotifyRaw(ctx, "session/update", json.RawMessage(`{"sessionId":"same-id","update":{"sessionUpdate":"agent_message_chunk","text":"NEW"}}`)); err != nil {
						return nil, err
					}
					return json.RawMessage(`{"stopReason":"end_turn"}`), nil
				})
			})
			runtime := NewRuntime(factory, RuntimeOptions{RequireFreshConnectionPerTurn: retire == "disposed"})
			defer runtime.Close(context.Background())
			options := StartSessionOptions{Agent: Agent{Type: "fixture", Command: "unused"}}
			oldSession, err := runtime.StartSession(context.Background(), options)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			oldTurn := oldSession.StartTurn(ctx, RuntimePrompt{Text: "old"})
			<-oldStarted
			if retire == "cancelled" {
				cancel()
			}
			select {
			case result := <-oldTurn.Completion:
				if (retire == "cancelled") != (result.Err != nil) {
					t.Fatalf("unexpected first result: %+v", result)
				}
			case <-time.After(time.Second):
				t.Fatal("old turn did not retire")
			}
			if result := <-oldSession.StartTurn(context.Background(), RuntimePrompt{Text: "reuse"}).Completion; result.Err == nil {
				t.Fatal("retired handle accepted another turn")
			}
			fresh, err := runtime.StartSession(context.Background(), options)
			if err != nil {
				t.Fatal(err)
			}
			newTurn := fresh.StartTurn(context.Background(), RuntimePrompt{Text: "new"})
			<-newStarted
			if err := oldProvider.NotifyRaw(context.Background(), "session/update", json.RawMessage(`{"sessionId":"same-id","update":{"sessionUpdate":"agent_message_chunk","text":"OLD"}}`)); err == nil {
				t.Fatal("retired transport accepted a stale update")
			}
			// Also cover an already-decoded callback arriving after disposal.
			oldSession.driver.(*acpSessionDriver).handleSessionUpdate(SessionNotification{SessionID: "same-id", Update: SessionUpdate{SessionUpdate: "agent_message_chunk", Text: "OLD-CALLBACK"}})
			release()
			select {
			case result := <-newTurn.Completion:
				if result.Err != nil || result.Completion.OutputText != "NEW" {
					t.Fatalf("old stream polluted same-ID fresh session: %+v", result)
				}
			case <-time.After(time.Second):
				t.Fatal("fresh turn did not complete")
			}
			select {
			case update := <-fresh.Updates():
				t.Fatalf("old stream leaked a fresh session update: %+v", update)
			default:
			}
		})
	}
}

func TestAdversarialBufferedPostTerminalFrameCannotEnterNextTurn(t *testing.T) {
	driver := newOrphanTestDriver()
	providerReader, hostWriter := io.Pipe()
	hostReader, providerWriter := io.Pipe()
	lateRead := make(chan struct{})
	releaseLate := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseLate) }) }
	defer release()
	host := NewPeer(hostReader, hostWriter, PeerOptions{OnRawMessage: func(direction string, raw json.RawMessage) {
		if direction == "inbound" && strings.Contains(string(raw), "LATE_BUFFERED") {
			close(lateRead)
			<-releaseLate
		}
	}})
	lateWriter := &writeAfterStopReason{w: providerWriter, extra: []byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"agent_message_chunk","text":"LATE_BUFFERED"}}}` + "\n")}
	provider := NewPeer(providerReader, lateWriter, PeerOptions{})
	defer providerWriter.Close()
	defer host.Close()
	defer provider.Close()
	var prompts atomic.Int32
	provider.RegisterRequest("session/prompt", func(ctx context.Context, _ json.RawMessage) (any, error) {
		if prompts.Add(1) > 1 {
			if err := provider.NotifyRaw(ctx, "session/update", json.RawMessage(`{"sessionId":"session-1","update":{"sessionUpdate":"agent_message_chunk","text":"NEW"}}`)); err != nil {
				return nil, err
			}
		}
		return json.RawMessage(`{"stopReason":"end_turn"}`), nil
	})
	connection := NewConnection(host, Client{})
	connection.SetSessionUpdateHandler(func(_ context.Context, update SessionNotification) { driver.handleSessionUpdate(update) })
	driver.connection = connection
	go host.Start(context.Background())
	go provider.Start(context.Background())
	first := driver.StartTurn(context.Background(), RuntimePrompt{Text: "first"})
	select {
	case <-lateRead:
	case <-time.After(time.Second):
		t.Fatal("same-write post-terminal frame did not reach read-loop barrier")
	}
	var next TurnHandle
	early := false
	select {
	case result := <-first.Completion:
		if result.Err != nil {
			t.Fatal(result.Err)
		}
		early = true
		next = driver.StartTurn(context.Background(), RuntimePrompt{Text: "next"})
	case <-time.After(30 * time.Millisecond):
	}
	release()
	if !early {
		select {
		case result := <-first.Completion:
			if result.Err != nil {
				t.Fatal(result.Err)
			}
		case <-time.After(time.Second):
			t.Fatal("completion did not release after fixed buffered-frame drain")
		}
		next = driver.StartTurn(context.Background(), RuntimePrompt{Text: "next"})
	}
	select {
	case result := <-next.Completion:
		if result.Err != nil || result.Completion.OutputText != "NEW" {
			t.Errorf("already-buffered old frame entered new turn: %+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("next turn did not complete")
	}
	if early {
		t.Error("completion admitted another turn before fixed already-buffered frame dispatch")
	}
}

func TestAdversarialTerminalReceiveFenceCountsIgnoredEmptyLines(t *testing.T) {
	factory := reviewPipeFactory(t, func(provider *Peer) {
		provider.writer = &writeAfterStopReason{w: provider.writer, extra: []byte("\n")}
		provider.RegisterRequest("session/new", func(context.Context, json.RawMessage) (any, error) {
			return json.RawMessage(`{"sessionId":"s"}`), nil
		})
		provider.RegisterRequest("session/prompt", func(context.Context, json.RawMessage) (any, error) {
			return json.RawMessage(`{"stopReason":"end_turn"}`), nil
		})
	})
	runtime := NewRuntime(factory, RuntimeOptions{})
	defer runtime.Close(context.Background())
	session, err := runtime.StartSession(context.Background(), StartSessionOptions{Agent: Agent{Type: "fixture", Command: "unused"}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if _, err := session.Run(ctx, "first"); err != nil {
		t.Fatalf("finite receive fence waited for future traffic after buffered empty line: %v", err)
	}
}
