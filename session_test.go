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

func TestMCPServerMarshalIncludesRequiredEmptyArrays(t *testing.T) {
	tests := []struct {
		name string
		in   MCPServer
		keys []string
	}{
		{
			name: "stdio",
			in:   MCPServer{Name: "fs", Type: "stdio", Command: "mcp-server"},
			keys: []string{"args", "env"},
		},
		{
			name: "http",
			in:   MCPServer{Name: "remote", Type: "http", URL: "https://example.com/mcp"},
			keys: []string{"headers"},
		},
		{
			name: "sse",
			in:   MCPServer{Name: "events", Type: "sse", URL: "https://example.com/sse"},
			keys: []string{"headers"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bytes, err := json.Marshal(tt.in)
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}
			var got map[string]json.RawMessage
			if err := json.Unmarshal(bytes, &got); err != nil {
				t.Fatalf("Unmarshal() error = %v", err)
			}
			for _, key := range tt.keys {
				raw, ok := got[key]
				if !ok {
					t.Fatalf("MCPServer JSON = %s, missing %q", bytes, key)
				}
				if string(raw) != "[]" {
					t.Fatalf("MCPServer JSON %q = %s, want []", key, raw)
				}
			}
			if tt.name == "stdio" {
				if _, ok := got["type"]; ok {
					t.Fatalf("MCPServer JSON = %s, want no type field for stdio", bytes)
				}
			}
		})
	}
}

func TestSetSessionConfigOptionUsesConfigIDWireField(t *testing.T) {
	req := SetSessionConfigOptionRequest{SessionID: "s1", OptionID: "model", Value: "opus"}
	bytes, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(bytes, &got); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if _, ok := got["configId"]; !ok {
		t.Fatalf("SetSessionConfigOptionRequest JSON = %s, missing configId", bytes)
	}
	if _, ok := got["optionId"]; ok {
		t.Fatalf("SetSessionConfigOptionRequest JSON = %s, want no optionId", bytes)
	}
}

func TestSessionUpdateTextPreservesWhitespaceOnlyChunks(t *testing.T) {
	raw := []byte(`{"sessionId":"s1","update":{"sessionUpdate":"agent_message_chunk","type":"text","text":"\n\n"}}`)
	var notification SessionNotification
	if err := json.Unmarshal(raw, &notification); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got := sessionUpdateText(notification.Update); got != "\n\n" {
		t.Fatalf("sessionUpdateText() = %q, want newline chunk", got)
	}
}

func TestSessionDriverPreservesMarkdownWhitespaceChunks(t *testing.T) {
	active := &activeTurn{
		id:         "turn-1",
		events:     make(chan TurnEvent, 8),
		completion: make(chan TurnResult, 1),
	}
	driver := &acpSessionDriver{
		sessionID:   "session-1",
		currentTurn: active,
		status:      "running",
		metadata:    RuntimeSessionMetadata{SessionID: "session-1"},
		toolCalls:   map[string]ToolCallSnapshot{},
		operations:  map[string]Operation{},
		permissions: map[string]PermissionRequestSnapshot{},
		rawConfig:   map[string]any{},
	}
	for _, text := range []string{"## 标题", "\n\n", "```javascript", "\n", "function renderMessages() {}", "\n", "```", "\n"} {
		driver.handleSessionUpdate(SessionNotification{
			SessionID: "session-1",
			Update: SessionUpdate{
				SessionUpdate: "agent_message_chunk",
				Type:          "text",
				Text:          text,
			},
		})
	}
	driver.mu.RLock()
	got := active.outputText.String()
	driver.mu.RUnlock()
	want := "## 标题\n\n```javascript\nfunction renderMessages() {}\n```\n"
	if got != want {
		t.Fatalf("outputText = %q, want %q", got, want)
	}
	var gotEvents []string
	for len(active.events) > 0 {
		gotEvents = append(gotEvents, (<-active.events).Text)
	}
	if len(gotEvents) != 8 || gotEvents[1] != "\n\n" || gotEvents[3] != "\n" {
		t.Fatalf("text events = %#v, want whitespace chunks preserved", gotEvents)
	}
}

func TestSetSessionConfigOptionAcceptsLegacyOptionID(t *testing.T) {
	var req SetSessionConfigOptionRequest
	raw := []byte(`{"sessionId":"s1","optionId":"model","value":"opus"}`)
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if req.OptionID != "model" {
		t.Fatalf("OptionID = %q, want model", req.OptionID)
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

func TestHandleSessionUpdateEmitsOrphanWhenNoTurn(t *testing.T) {
	driver := &acpSessionDriver{
		sessionID:   "session-1",
		status:      "ready",
		toolCalls:   map[string]ToolCallSnapshot{},
		operations:  map[string]Operation{},
		permissions: map[string]PermissionRequestSnapshot{},
		rawConfig:   map[string]any{},
		updates:     make(chan SessionNotification, 64),
	}
	driver.handleSessionUpdate(SessionNotification{
		SessionID: "session-1",
		Update: SessionUpdate{
			SessionUpdate: "agent_message_chunk",
			Type:          "text",
			Text:          "follow-up",
		},
	})
	select {
	case got := <-driver.SessionUpdates():
		if got.Update.Text != "follow-up" {
			t.Fatalf("orphan update text = %q, want follow-up", got.Update.Text)
		}
		if got.UpdateID == "" {
			t.Fatal("orphan update must carry UpdateID")
		}
		if got.Terminal {
			t.Fatal("body chunk must not close the generation")
		}
	default:
		t.Fatal("expected orphan session update")
	}
}

func TestOrphanUpdatesShareGenerationWithoutMessageID(t *testing.T) {
	driver := newOrphanTestDriver()
	driver.handleSessionUpdate(SessionNotification{
		SessionID: "session-1",
		Update:    SessionUpdate{SessionUpdate: "agent_thought_chunk", Text: "想"},
	})
	driver.handleSessionUpdate(SessionNotification{
		SessionID: "session-1",
		Update:    SessionUpdate{SessionUpdate: "agent_message_chunk", Text: "按时"},
	})
	first := recvOrphan(t, driver)
	second := recvOrphan(t, driver)
	if first.UpdateID == "" || first.UpdateID != second.UpdateID {
		t.Fatalf("generation ids = %q %q", first.UpdateID, second.UpdateID)
	}
	if !strings.HasPrefix(first.UpdateID, "orphan:session-1:") {
		t.Fatalf("UpdateID = %q, want SDK generation", first.UpdateID)
	}
}

func TestOrphanUpdateUsesProtocolMessageID(t *testing.T) {
	driver := newOrphanTestDriver()
	driver.handleSessionUpdate(SessionNotification{
		SessionID: "session-1",
		Update: SessionUpdate{
			SessionUpdate: "agent_message_chunk",
			MessageID:     "msg_agent_c42b9",
			Text:          "吃药",
		},
	})
	got := recvOrphan(t, driver)
	if got.UpdateID != "msg_agent_c42b9" {
		t.Fatalf("UpdateID = %q, want protocol messageId", got.UpdateID)
	}
}

func TestOrphanAvailableCommandsClosesGeneration(t *testing.T) {
	driver := newOrphanTestDriver()
	driver.handleSessionUpdate(SessionNotification{
		SessionID: "session-1",
		Update:    SessionUpdate{SessionUpdate: "agent_message_chunk", Text: "完成"},
	})
	body := recvOrphan(t, driver)
	driver.handleSessionUpdate(SessionNotification{
		SessionID: "session-1",
		Update:    SessionUpdate{SessionUpdate: "available_commands_update"},
	})
	idle := recvOrphan(t, driver)
	if !idle.Terminal || idle.UpdateID != body.UpdateID {
		t.Fatalf("idle close = %#v body=%q", idle, body.UpdateID)
	}
	driver.handleSessionUpdate(SessionNotification{
		SessionID: "session-1",
		Update:    SessionUpdate{SessionUpdate: "agent_message_chunk", Text: "下一轮"},
	})
	next := recvOrphan(t, driver)
	if next.UpdateID == "" || next.UpdateID == body.UpdateID {
		t.Fatalf("next generation = %q, previous = %q", next.UpdateID, body.UpdateID)
	}
}

func TestOrphanMessageIDChangeClosesPrevious(t *testing.T) {
	driver := newOrphanTestDriver()
	driver.handleSessionUpdate(SessionNotification{
		SessionID: "session-1",
		Update:    SessionUpdate{SessionUpdate: "agent_message_chunk", MessageID: "msg-1", Text: "一"},
	})
	driver.handleSessionUpdate(SessionNotification{
		SessionID: "session-1",
		Update:    SessionUpdate{SessionUpdate: "agent_message_chunk", MessageID: "msg-2", Text: "二"},
	})
	first := recvOrphan(t, driver)
	closed := recvOrphan(t, driver)
	second := recvOrphan(t, driver)
	if first.UpdateID != "msg-1" || second.UpdateID != "msg-2" {
		t.Fatalf("ids = %q %q", first.UpdateID, second.UpdateID)
	}
	if !closed.Terminal || closed.UpdateID != "msg-1" {
		t.Fatalf("close = %#v", closed)
	}
}

func TestStartTurnClosesOpenOrphanGeneration(t *testing.T) {
	driver := newOrphanTestDriver()
	connectPromptProvider(t, driver)
	driver.handleSessionUpdate(SessionNotification{
		SessionID: "session-1",
		Update:    SessionUpdate{SessionUpdate: "agent_message_chunk", Text: "后台"},
	})
	body := recvOrphan(t, driver)
	handle := driver.StartTurn(context.Background(), RuntimePrompt{Text: "next"})
	closed := recvOrphanWait(t, driver, time.Second)
	if !closed.Terminal || closed.UpdateID != body.UpdateID {
		t.Fatalf("start turn close = %#v body=%q", closed, body.UpdateID)
	}
	result := <-handle.Completion
	if result.Err != nil {
		t.Fatalf("StartTurn completion err = %v", result.Err)
	}
}

func TestCloseClosesOpenOrphanGeneration(t *testing.T) {
	driver := newOrphanTestDriver()
	peer := NewPeer(neverEOFReader{}, discardWriter{}, PeerOptions{})
	driver.connection = NewConnection(peer, Client{})
	driver.handleSessionUpdate(SessionNotification{
		SessionID: "session-1",
		Update:    SessionUpdate{SessionUpdate: "agent_message_chunk", Text: "后台"},
	})
	body := recvOrphan(t, driver)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- driver.Close(ctx) }()
	closed := recvOrphanWait(t, driver, time.Second)
	if !closed.Terminal || closed.UpdateID != body.UpdateID {
		t.Fatalf("close terminal = %#v body=%q", closed, body.UpdateID)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Close did not return")
	}
}

func TestOrphanBackgroundFollowUpSharesGenerationThenCloses(t *testing.T) {
	driver := newOrphanTestDriver()
	status := "completed"
	title := "sleep 60 && echo ORPHAN-BG-DONE-42"
	kind := "execute"
	driver.handleSessionUpdate(SessionNotification{
		SessionID: "session-1",
		Update:    SessionUpdate{SessionUpdate: "tool_call_update", ToolCallID: "bg-sleep", Title: &title, Kind: &kind, Status: &status},
	})
	for _, text := range []string{"OR", "PHAN", "-BG-DONE"} {
		driver.handleSessionUpdate(SessionNotification{
			SessionID: "session-1",
			Update:    SessionUpdate{SessionUpdate: "agent_message_chunk", Text: text},
		})
	}
	driver.handleSessionUpdate(SessionNotification{
		SessionID: "session-1",
		Update:    SessionUpdate{SessionUpdate: "available_commands_update"},
	})
	var events []SessionNotification
	for i := 0; i < 5; i++ {
		events = append(events, recvOrphan(t, driver))
	}
	id := events[0].UpdateID
	if id == "" {
		t.Fatal("follow-up generation id is empty")
	}
	var text strings.Builder
	for i, event := range events {
		if event.UpdateID != id {
			t.Fatalf("event[%d] UpdateID = %q, want %q", i, event.UpdateID, id)
		}
		if i < 4 && event.Terminal {
			t.Fatalf("event[%d] closed generation early: %#v", i, event)
		}
		text.WriteString(event.Update.Text)
	}
	if !events[4].Terminal || events[4].Update.SessionUpdate != "available_commands_update" {
		t.Fatalf("closer = %#v", events[4])
	}
	if text.String() != "ORPHAN-BG-DONE" {
		t.Fatalf("concatenated body text = %q", text.String())
	}
}

func TestOrphanConfigUpdateDoesNotCloseGeneration(t *testing.T) {
	driver := newOrphanTestDriver()
	driver.handleSessionUpdate(SessionNotification{
		SessionID: "session-1",
		Update:    SessionUpdate{SessionUpdate: "agent_message_chunk", Text: "进行中"},
	})
	body := recvOrphan(t, driver)
	driver.handleSessionUpdate(SessionNotification{
		SessionID: "session-1",
		Update:    SessionUpdate{SessionUpdate: "config_option_update"},
	})
	meta := recvOrphan(t, driver)
	if meta.Terminal || meta.UpdateID != body.UpdateID {
		t.Fatalf("config update = %#v", meta)
	}
	driver.handleSessionUpdate(SessionNotification{
		SessionID: "session-1",
		Update:    SessionUpdate{SessionUpdate: "agent_message_chunk", Text: "续"},
	})
	cont := recvOrphan(t, driver)
	if cont.UpdateID != body.UpdateID || cont.Terminal {
		t.Fatalf("continuation = %#v", cont)
	}
}

func TestOrphanLateMessageIDKeepsGeneration(t *testing.T) {
	driver := newOrphanTestDriver()
	driver.handleSessionUpdate(SessionNotification{
		SessionID: "session-1",
		Update:    SessionUpdate{SessionUpdate: "agent_message_chunk", Text: "先"},
	})
	first := recvOrphan(t, driver)
	driver.handleSessionUpdate(SessionNotification{
		SessionID: "session-1",
		Update: SessionUpdate{
			SessionUpdate: "agent_message_chunk",
			MessageID:     "msg_late",
			Text:          "后",
		},
	})
	second := recvOrphan(t, driver)
	if first.UpdateID != second.UpdateID {
		t.Fatalf("late messageId rewrote UpdateID %q -> %q", first.UpdateID, second.UpdateID)
	}
	if !strings.HasPrefix(first.UpdateID, "orphan:session-1:") {
		t.Fatalf("UpdateID = %q, want stable SDK generation", first.UpdateID)
	}
}

func TestSessionUpdateUnmarshalsMessageID(t *testing.T) {
	var notification SessionNotification
	raw := []byte(`{"sessionId":"s1","update":{"sessionUpdate":"agent_message_chunk","messageId":"msg_1","content":{"type":"text","text":"OK"}}}`)
	if err := json.Unmarshal(raw, &notification); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if notification.Update.MessageID != "msg_1" {
		t.Fatalf("MessageID = %q", notification.Update.MessageID)
	}
}

func newOrphanTestDriver() *acpSessionDriver {
	return &acpSessionDriver{
		sessionID:   "session-1",
		status:      "ready",
		toolCalls:   map[string]ToolCallSnapshot{},
		operations:  map[string]Operation{},
		permissions: map[string]PermissionRequestSnapshot{},
		rawConfig:   map[string]any{},
		updates:     make(chan SessionNotification, 64),
	}
}

func recvOrphan(t *testing.T, driver *acpSessionDriver) SessionNotification {
	t.Helper()
	select {
	case got := <-driver.SessionUpdates():
		return got
	default:
		t.Fatal("expected orphan session update")
		return SessionNotification{}
	}
}

func recvOrphanWait(t *testing.T, driver *acpSessionDriver, wait time.Duration) SessionNotification {
	t.Helper()
	select {
	case got := <-driver.SessionUpdates():
		return got
	case <-time.After(wait):
		t.Fatal("timed out waiting for orphan session update")
		return SessionNotification{}
	}
}

func connectPromptProvider(t *testing.T, driver *acpSessionDriver) {
	t.Helper()
	providerReader, runtimeWriter := io.Pipe()
	runtimeReader, providerWriter := io.Pipe()
	runtimePeer := NewPeer(runtimeReader, runtimeWriter, PeerOptions{})
	providerPeer := NewPeer(providerReader, providerWriter, PeerOptions{})
	providerPeer.RegisterRequest("session/prompt", func(context.Context, json.RawMessage) (any, error) {
		return PromptResponse{StopReason: "end_turn"}, nil
	})
	driver.connection = NewConnection(runtimePeer, Client{})
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
}

func TestHandleSessionUpdateDoesNotEmitOrphanDuringTurn(t *testing.T) {
	active := &activeTurn{
		id:         "turn-1",
		events:     make(chan TurnEvent, 8),
		completion: make(chan TurnResult, 1),
	}
	driver := &acpSessionDriver{
		sessionID:   "session-1",
		currentTurn: active,
		status:      "running",
		toolCalls:   map[string]ToolCallSnapshot{},
		operations:  map[string]Operation{},
		permissions: map[string]PermissionRequestSnapshot{},
		rawConfig:   map[string]any{},
		updates:     make(chan SessionNotification, 64),
	}
	driver.handleSessionUpdate(SessionNotification{
		SessionID: "session-1",
		Update: SessionUpdate{
			SessionUpdate: "agent_message_chunk",
			Text:          "in-turn",
		},
	})
	select {
	case <-driver.SessionUpdates():
		t.Fatal("in-turn update must not go to SessionUpdates")
	default:
	}
	select {
	case event := <-active.events:
		if event.Text != "in-turn" {
			t.Fatalf("turn event text = %q, want in-turn", event.Text)
		}
	default:
		t.Fatal("expected turn event")
	}
}

func TestHandleSessionUpdateDoesNotEmitOrphanWhenDropDelivery(t *testing.T) {
	active := &activeTurn{
		id:               "turn-1",
		events:           make(chan TurnEvent, 1),
		completion:       make(chan TurnResult, 1),
		dropIntermediate: true,
	}
	driver := &acpSessionDriver{
		sessionID:   "session-1",
		currentTurn: active,
		updates:     make(chan SessionNotification, 8),
		toolCalls:   map[string]ToolCallSnapshot{},
		operations:  map[string]Operation{},
		permissions: map[string]PermissionRequestSnapshot{},
		rawConfig:   map[string]any{},
	}
	driver.handleSessionUpdate(SessionNotification{
		SessionID: "session-1",
		Update: SessionUpdate{
			SessionUpdate: "agent_message_chunk",
			Text:          "suppressed",
		},
	})
	select {
	case <-driver.SessionUpdates():
		t.Fatal("drop delivery must not emit orphan updates")
	default:
	}
	if got := active.outputText.String(); got != "suppressed" {
		t.Fatalf("outputText = %q, want suppressed", got)
	}
}

func TestEmitOrphanSessionUpdateDropsWhenBufferFull(t *testing.T) {
	var dropped []RuntimeEventDrop
	driver := &acpSessionDriver{
		sessionID: "session-1",
		updates:   make(chan SessionNotification, 1),
		hooks: RuntimeHooks{
			OnEventDrop: func(drop RuntimeEventDrop) {
				dropped = append(dropped, drop)
			},
		},
	}
	driver.emitOrphanSessionUpdate(SessionNotification{
		SessionID: "session-1",
		Update:    SessionUpdate{SessionUpdate: "agent_message_chunk", Text: "keep"},
	})
	driver.emitOrphanSessionUpdate(SessionNotification{
		SessionID: "session-1",
		Update:    SessionUpdate{SessionUpdate: "agent_message_chunk", Text: "drop"},
	})
	if len(dropped) != 1 {
		t.Fatalf("OnEventDrop count = %d, want 1", len(dropped))
	}
	if dropped[0].SessionID != "session-1" || dropped[0].EventType != "agent_message_chunk" {
		t.Fatalf("drop = %#v", dropped[0])
	}
	if got := <-driver.SessionUpdates(); got.Update.Text != "keep" {
		t.Fatalf("buffered update = %q, want keep", got.Update.Text)
	}
}

func TestSessionUpdatesWiresDriverChannel(t *testing.T) {
	updates := make(chan SessionNotification, 1)
	session := newSession(nil, &acpSessionDriver{updates: updates})
	if session.Updates() != updates {
		t.Fatal("Session.Updates() is not wired to the driver channel")
	}
}

func TestSessionUpdatesFallsBackToDriver(t *testing.T) {
	updates := make(chan SessionNotification, 1)
	session := &Session{driver: &acpSessionDriver{updates: updates}}
	if session.Updates() != updates {
		t.Fatal("Session.Updates() should fall back to driver.SessionUpdates()")
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

func (d *testSessionDriver) Delete(context.Context) error { return nil }

func (d *testSessionDriver) Logout(context.Context) error { return nil }

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
