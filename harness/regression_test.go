package harness

import (
	"context"
	"encoding/json"
	"errors"
	acp "github.com/saaskit-dev/acp-runtime-go"
	"io"
	"testing"
)

func TestHarnessRejectsFabricatedEvidence(t *testing.T) {
	c := Case{ID: "fabricated", Steps: []CaseStep{{Type: "initialize"}, {Type: "authenticate"}, {Type: "permission-decision", Decision: "deny"}, {Type: "wait-for-event", EventType: "never-emitted"}}, Assertions: []Assertion{{Type: "transcript-has-event", EventType: "never-emitted"}}}
	if _, err := (Runner{CWD: t.TempDir()}).Run(context.Background(), c); err == nil {
		t.Fatal("case with no agent/session passed using manufactured evidence")
	}
}

func TestHarnessRejectsEmptyAssertions(t *testing.T) {
	for _, kind := range []string{"transcript-method-response-has", "transcript-order", "transcript-event-field", "transcript-event-count", "any-of"} {
		t.Run(kind, func(t *testing.T) {
			if err := validateAssertion(Result{}, Assertion{Type: kind}); err == nil {
				t.Fatal("empty assertion without evidence passed")
			}
		})
	}
}

func TestRawAssertionsRequireEvidence(t *testing.T) {
	rec := newRecorder()
	observe := func(direction, raw string) { rec.observe(1, direction, []byte(raw)) }
	req := `{"jsonrpc":"2.0","id":1,"method":"session/prompt","params":{"sessionId":"s","prompt":[]}}`
	observe("outbound_attempt", req)
	if hasMethod(rec.snapshot(), "session/prompt") {
		t.Fatal("attempt treated as delivery")
	}
	observe("outbound", req)
	if hasMethod(rec.snapshot(), "session/prompt") {
		t.Fatal("request without response passed")
	}
	observe("inbound", `{"jsonrpc":"2.0","id":2,"result":{"stopReason":"end_turn"}}`)
	if hasMethod(rec.snapshot(), "session/prompt") {
		t.Fatal("wrong response ID passed")
	}
	observe("inbound", `{"jsonrpc":"2.0","id":1,"result":{"stopReason":"end_turn"}}`)
	result := Result{Transcript: rec.snapshot()}
	if !hasMethod(result.Transcript, "session/prompt") {
		t.Fatal("matched response not recognized")
	}
	for _, a := range []Assertion{{Type: "transcript-method-response-has", Method: "session/prompt", Path: "stopReason", Equals: "cancelled"}, {Type: "transcript-event-field", EventType: "plan", Path: "update.entries.0.content", Equals: "wrong"}, {Type: "transcript-order", First: "session/cancel", Then: "session/prompt"}, {Type: "transcript-event-count", EventType: "completed", Min: intPtr(2)}} {
		if validateAssertion(result, a) == nil {
			t.Fatalf("invalid assertion passed: %+v", a)
		}
	}
	observe("outbound", `{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":"s"}}`)
	if validateAssertion(Result{Transcript: rec.snapshot()}, Assertion{Type: "transcript-order", First: "session/prompt", Then: "session/cancel"}) == nil {
		t.Fatal("cancel after terminal passed")
	}
}
func intPtr(n int) *int { return &n }
func TestPermissionEvidenceRejectsUnknownOption(t *testing.T) {
	rec := newRecorder()
	rec.observe(1, "inbound", []byte(`{"jsonrpc":"2.0","id":"p","method":"session/request_permission","params":{"sessionId":"s","toolCall":{"toolCallId":"t"},"options":[{"optionId":"deny","kind":"reject_once","name":"Deny"}]}}`))
	rec.observe(1, "outbound", []byte(`{"jsonrpc":"2.0","id":"p","result":{"outcome":{"outcome":"selected","optionId":"allow"}}}`))
	if validateAssertion(Result{Transcript: rec.snapshot()}, Assertion{Type: "transcript-has-method", Method: "session/request_permission"}) == nil {
		t.Fatal("unoffered permission option passed")
	}
}
func TestAssertionsInspectActualFields(t *testing.T) {
	rec := newRecorder()
	rec.observe(1, "inbound", []byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s","update":{"sessionUpdate":"tool_call_update","kind":"read","status":"failed"}}}`))
	result := Result{Transcript: rec.snapshot()}
	for _, a := range []Assertion{{Type: "transcript-event-field", EventType: "tool_call_update", Path: "update.status", Equals: "completed"}, {Type: "transcript-has-tool-update", Kind: "execute", Status: "completed"}, {Type: "transcript-event-count", EventType: "tool_call_update", Max: intPtr(0)}} {
		if validateAssertion(result, a) == nil {
			t.Fatalf("wrong field passed: %+v", a)
		}
	}
	if err := validateAssertion(result, Assertion{Type: "transcript-event-field", EventType: "tool_call_update", Path: "update.status", Equals: "failed"}); err != nil {
		t.Fatal(err)
	}
}

func TestCleanupFailureCannotRetainPassStatus(t *testing.T) {
	factory := func(ctx context.Context, input acp.ConnectionFactoryInput) (acp.ConnectionHandle, error) {
		clientRead, agentWrite := io.Pipe()
		agentRead, clientWrite := io.Pipe()
		server := acp.NewPeer(agentRead, agentWrite, acp.PeerOptions{})
		server.RegisterRequest("initialize", func(context.Context, json.RawMessage) (any, error) { return map[string]any{"protocolVersion": 1}, nil })
		peer := acp.NewPeer(clientRead, clientWrite, acp.PeerOptions{})
		go server.Start(ctx)
		go peer.Start(ctx)
		return acp.ConnectionHandle{Connection: acp.NewConnection(peer, input.Client), Dispose: func(context.Context) error {
			peer.Close()
			server.Close()
			clientRead.Close()
			agentWrite.Close()
			agentRead.Close()
			clientWrite.Close()
			return errors.New("fixture cleanup failed")
		}}, nil
	}
	result, err := (Runner{Factory: factory, Agent: acp.Agent{Command: "fixture"}, CWD: t.TempDir()}).Run(context.Background(), Case{ID: "cleanup", Steps: []CaseStep{{Type: "initialize"}}, Assertions: []Assertion{{Type: "transcript-has-method", Method: "initialize"}}})
	if err == nil || result.Status != "FAIL" {
		t.Fatalf("cleanup error was hidden: %+v %v", result, err)
	}
}

func TestMalformedWireRemainsVisibleAndFails(t *testing.T) {
	rec := newRecorder()
	rec.observe(1, "inbound", []byte(`{broken`))
	result := Result{Transcript: rec.snapshot()}
	if len(result.Transcript) != 1 || result.Transcript[0].Malformed != "{broken" {
		t.Fatal("malformed frame disappeared")
	}
	if validateAssertions(Case{ID: "malformed"}, result) == nil {
		t.Fatal("malformed frame accepted")
	}
	if _, err := json.Marshal(result); err != nil {
		t.Fatalf("malformed evidence is not serializable: %v", err)
	}
}

func TestCancelOrderUsesMatchingPromptIdentity(t *testing.T) {
	rec := newRecorder()
	for _, frame := range []struct{ direction, raw string }{{"outbound", `{"jsonrpc":"2.0","id":1,"method":"session/prompt","params":{"sessionId":"s"}}`}, {"inbound", `{"jsonrpc":"2.0","id":1,"result":{"stopReason":"end_turn"}}`}, {"outbound", `{"jsonrpc":"2.0","id":2,"method":"session/prompt","params":{"sessionId":"s"}}`}, {"outbound", `{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":"s"}}`}, {"inbound", `{"jsonrpc":"2.0","id":2,"result":{"stopReason":"cancelled"}}`}} {
		rec.observe(1, frame.direction, []byte(frame.raw))
	}
	if err := validateAssertion(Result{Transcript: rec.snapshot()}, Assertion{Type: "transcript-order", First: "session/prompt", Then: "session/cancel"}); err != nil {
		t.Fatalf("prior turn incorrectly blocked second-turn cancel: %v", err)
	}
}
func TestOrderCannotBorrowOtherRequestResponse(t *testing.T) {
	rec := newRecorder()
	for _, frame := range []struct{ direction, raw string }{{"outbound", `{"jsonrpc":"2.0","id":1,"method":"first","params":{}}`}, {"outbound", `{"jsonrpc":"2.0","method":"then","params":{}}`}, {"outbound", `{"jsonrpc":"2.0","id":2,"method":"first","params":{}}`}, {"inbound", `{"jsonrpc":"2.0","id":2,"result":{}}`}} {
		rec.observe(1, frame.direction, []byte(frame.raw))
	}
	if validateAssertion(Result{Transcript: rec.snapshot()}, Assertion{Type: "transcript-order", First: "first", Then: "then"}) == nil {
		t.Fatal("unanswered first request borrowed later response")
	}
}

func TestWaitCannotUseForeignSessionEvent(t *testing.T) {
	factory := func(ctx context.Context, input acp.ConnectionFactoryInput) (acp.ConnectionHandle, error) {
		clientRead, agentWrite := io.Pipe()
		agentRead, clientWrite := io.Pipe()
		server := acp.NewPeer(agentRead, agentWrite, acp.PeerOptions{})
		peer := acp.NewPeer(clientRead, clientWrite, acp.PeerOptions{})
		server.RegisterRequest("initialize", func(context.Context, json.RawMessage) (any, error) {
			return map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{"sessionCapabilities": map[string]any{"close": map[string]any{}}}}, nil
		})
		server.RegisterRequest("session/new", func(context.Context, json.RawMessage) (any, error) {
			return map[string]any{"sessionId": "current"}, nil
		})
		server.RegisterRequest("session/close", func(context.Context, json.RawMessage) (any, error) { return map[string]any{}, nil })
		server.RegisterRequest("session/prompt", func(ctx context.Context, _ json.RawMessage) (any, error) {
			if err := server.Notify(ctx, "session/update", map[string]any{"sessionId": "foreign", "update": map[string]any{"sessionUpdate": "plan", "entries": []any{}}}); err != nil {
				return nil, err
			}
			return map[string]any{"stopReason": "end_turn"}, nil
		})
		go server.Start(ctx)
		go peer.Start(ctx)
		return acp.ConnectionHandle{Connection: acp.NewConnection(peer, input.Client), Dispose: func(context.Context) error {
			peer.Close()
			server.Close()
			clientRead.Close()
			agentWrite.Close()
			agentRead.Close()
			clientWrite.Close()
			return nil
		}}, nil
	}
	c := Case{ID: "foreign", Steps: []CaseStep{{Type: "session-new"}, {Type: "session-prompt", Prompt: "emit"}, {Type: "wait-for-event", EventType: "plan", TimeoutMS: 20}}}
	result, err := (Runner{Factory: factory, Agent: acp.Agent{Command: "fixture"}, CWD: t.TempDir()}).Run(context.Background(), c)
	if err == nil || result.Status != "FAIL" {
		t.Fatalf("foreign event satisfied current session wait: %+v %v", result, err)
	}
}
func TestNativeProbeWithoutFactoryIsExplicitlyUnsupported(t *testing.T) {
	result, err := (Runner{Agent: acp.Agent{Type: acp.CodexNativeRegistryID, Command: "never-spawn-native"}, CWD: t.TempDir()}).Run(context.Background(), Case{ID: "native-probe", Steps: []CaseStep{{Type: "initialize"}}})
	var unsupported *UnsupportedError
	if !errors.As(err, &unsupported) || result.Status != "UNSUPPORTED" {
		t.Fatalf("native probe attempted wrong transport: %+v %v", result, err)
	}
}
