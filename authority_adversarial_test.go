package acpruntime

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Review regressions intentionally assert the required fail-closed behavior.
func TestAuthorityReviewPermissionObserverCannotRestoreRevokedLease(t *testing.T) {
	var live atomic.Bool
	live.Store(true)
	c := stableConnection(t, Client{Authority: AuthorityHandlers{Permission: func(Context, PermissionRequest) (PermissionDecision, error) {
		return PermissionDecision{Outcome: "selected", OptionID: "approve-one"}, nil
	}}})
	c.SetPermissionLease(func(PermissionRequest) func() bool { return live.Load })
	c.SetPermissionObserver(func(PermissionRequest, PermissionDecision) { live.Store(false) })
	result, err := stableHandler(t, c, "session/request_permission")(context.Background(), stableFixture(t, "permission-request"))
	if err != nil {
		t.Fatal(err)
	}
	if got := result.(PermissionDecision); got.Outcome != "cancelled" {
		t.Fatalf("revoked authority escaped after observer: %+v", got)
	}
}

func TestAuthorityReviewClaudeRepeatedIDCannotReauthorizeOldReply(t *testing.T) {
	firstEntered := make(chan struct{})
	firstRelease, thirdRelease := make(chan struct{}), make(chan struct{})
	defer close(thirdRelease)
	permissionCtx, cancelPermission := context.WithCancel(context.Background())
	defer cancelPermission()
	capture := &nativePermissionCapture{notify: make(chan struct{}, 8)}
	proc := &claudeProc{acpSessionID: "session", stdin: capture, turn: &claudeTurn{done: make(chan struct{}), permissionCtx: permissionCtx, cancelPermission: cancelPermission}}
	var calls atomic.Int32
	proc.eng = &claudeNativeEngine{opts: nativeEngineOptions{requestPermission: func(_ context.Context, _ PermissionRequest) (PermissionDecision, error) {
		if calls.Add(1) == 1 {
			close(firstEntered)
			<-firstRelease
		} else {
			<-thirdRelease
		}
		return PermissionDecision{Outcome: "selected", OptionID: "allow"}, nil
	}}}
	line := []byte(`{"type":"control_request","request_id":"reused","request":{"subtype":"can_use_tool","tool_name":"Bash","tool_use_id":"tool-1","input":{"command":"echo fixture"}}}`)
	proc.handleLine(line)
	select {
	case <-firstEntered:
	case <-time.After(time.Second):
		t.Fatal("first authority not entered")
	}
	proc.handleLine(line) // revoke first request via duplicate ID
	proc.handleLine(line) // must not resurrect the original request's identity
	close(firstRelease)
	deadline := time.After(time.Second)
	for {
		capture.mu.Lock()
		count := len(capture.lines)
		for _, line := range capture.lines {
			if strings.Contains(string(line), `"behavior":"allow"`) {
				capture.mu.Unlock()
				t.Fatal("revoked first approval became allowed when the ID was reused")
			}
		}
		capture.mu.Unlock()
		if count >= 3 {
			return
		}
		select {
		case <-capture.notify:
		case <-deadline:
			t.Fatal("original approval did not settle")
		}
	}
}

func TestAuthorityReviewUnknownRequestScopedElicitationFailsClosed(t *testing.T) {
	var calls atomic.Int32
	c := stableConnection(t, Client{Authority: AuthorityHandlers{Elicitation: ElicitationHandlers{Form: func(Context, ElicitationRequest) (ElicitationResponse, error) {
		calls.Add(1)
		return ElicitationResponse{Action: "accept", Content: map[string]any{"confirm": true}}, nil
	}}}})
	_ = newACPSessionDriver(sessionBootstrap{Connection: c, SessionResponse: NewSessionResponse{SessionID: "session"}, Profile: defaultAgentProfile()})
	result, err := stableHandler(t, c, "elicitation/create")(context.Background(), json.RawMessage(`{"mode":"form","message":"Orphan request","requestId":"already-finished","requestedSchema":{"type":"object","properties":{"confirm":{"type":"boolean"}},"required":["confirm"]}}`))
	if err == nil && result.(ElicitationResponse).Action == "accept" {
		t.Errorf("nonexistent request scope accepted: %+v", result)
	}
	if calls.Load() != 0 {
		t.Errorf("orphan request invoked host authority %d times", calls.Load())
	}
}

func TestAuthorityReviewUnserializableCodexExtraCannotDropSandbox(t *testing.T) {
	cycle := map[string]any{}
	cycle["self"] = cycle
	for _, typ := range []string{CodexACPRegistryID, CodexNativeRegistryID} {
		for name, value := range map[string]any{"channel": make(chan int), "function": func() {}, "cycle": cycle} {
			t.Run(typ+"/"+name, func(t *testing.T) {
				agent := Agent{Type: typ}
				projected, _, err := prepareAgentSessionStart(ResolveAgentProfile(agent), StartSessionOptions{Agent: agent, AgentConfig: &AgentConfig{Sandbox: "read-only", Extra: map[string]any{"custom": value}}})
				if err == nil {
					t.Errorf("%s accepted unserializable config, losing requested sandbox: CODEX_CONFIG=%q", typ, projected.Env["CODEX_CONFIG"])
				}
			})
		}
	}
}

func authorityReviewOutgoingRequest(t *testing.T, c *Connection) (json.RawMessage, context.CancelFunc, <-chan error) {
	t.Helper()
	sent := make(chan json.RawMessage, 1)
	c.SetRawMessageObserver(func(direction string, raw json.RawMessage) {
		if direction != "outbound" {
			return
		}
		var msg rpcMessage
		if json.Unmarshal(raw, &msg) == nil && msg.Method == "authenticate" {
			sent <- append(json.RawMessage(nil), msg.ID...)
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.peer.Call(ctx, "authenticate", AuthenticateRequest{MethodID: "fixture"}, nil) }()
	select {
	case id := <-sent:
		return id, cancel, done
	case <-time.After(time.Second):
		cancel()
		t.Fatal("outgoing request was not written")
		return nil, nil, nil
	}
}

func TestAuthorityReviewParentRequestCancellationCancelsForm(t *testing.T) {
	entered := make(chan struct{})
	c := stableConnection(t, Client{Authority: AuthorityHandlers{Elicitation: ElicitationHandlers{Form: func(ctx Context, _ ElicitationRequest) (ElicitationResponse, error) {
		close(entered)
		<-ctx.Done()
		return ElicitationResponse{Action: "accept"}, nil
	}}}})
	id, cancel, requestDone := authorityReviewOutgoingRequest(t, c)
	defer cancel()
	result := make(chan ElicitationResponse, 1)
	raw := append([]byte(`{"mode":"form","message":"Question","requestedSchema":{"type":"object"},"requestId":`), id...)
	raw = append(raw, '}')
	go func() {
		response, err := stableHandler(t, c, "elicitation/create")(context.Background(), raw)
		if err != nil {
			result <- ElicitationResponse{Action: "error"}
			return
		}
		result <- response.(ElicitationResponse)
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("live parent request did not invoke form")
	}
	cancel()
	<-requestDone
	select {
	case response := <-result:
		if response.Action != "cancel" {
			t.Fatalf("parent-cancelled form result=%+v", response)
		}
	case <-time.After(time.Second):
		t.Fatal("form survived cancellation of its parent request")
	}
}

func TestAuthorityReviewCompletedParentRequestRevokesURLCompletion(t *testing.T) {
	delivered := make(chan string, 2)
	c := stableConnection(t, Client{Authority: AuthorityHandlers{Elicitation: ElicitationHandlers{URL: func(Context, ElicitationRequest) (ElicitationResponse, error) {
		return ElicitationResponse{Action: "accept"}, nil
	}, OnComplete: func(_ Context, event ElicitationCompletion) { delivered <- event.ElicitationID }}}})
	id, cancel, requestDone := authorityReviewOutgoingRequest(t, c)
	defer cancel()
	raw := append([]byte(`{"mode":"url","message":"Question","url":"https://example.test/fixture","elicitationId":"old-parent","requestId":`), id...)
	raw = append(raw, '}')
	response, err := stableHandler(t, c, "elicitation/create")(context.Background(), raw)
	if err != nil || response.(ElicitationResponse).Action != "accept" {
		t.Fatalf("live URL rejected: %+v %v", response, err)
	}
	c.peer.resolvePending(rpcMessage{ID: id, Result: json.RawMessage(`{}`)})
	if err := <-requestDone; err != nil {
		t.Fatal(err)
	}
	c.peer.handleNotification(context.Background(), rpcMessage{Method: "elicitation/complete", Params: json.RawMessage(`{"elicitationId":"old-parent"}`)})
	c.elicitation.deliverCompletion(ElicitationCompletion{ElicitationID: "queue-barrier"}, func() bool { return true })
	select {
	case got := <-delivered:
		if got != "queue-barrier" {
			t.Fatalf("completed parent still delivered URL completion: %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("completion queue barrier did not arrive")
	}
}

func TestAuthorityReviewReusedURLIDCannotCompleteNewScope(t *testing.T) {
	delivered := make(chan ElicitationCompletion, 2)
	c := stableConnection(t, Client{Authority: AuthorityHandlers{Elicitation: ElicitationHandlers{URL: func(Context, ElicitationRequest) (ElicitationResponse, error) {
		return ElicitationResponse{Action: "accept"}, nil
	}, OnComplete: func(_ Context, event ElicitationCompletion) { delivered <- event }}}})
	h := stableHandler(t, c, "elicitation/create")
	old := []byte(`{"mode":"url","message":"Old scope","sessionId":"old-session","url":"https://example.test/old","elicitationId":"reused"}`)
	fresh := []byte(`{"mode":"url","message":"New scope","sessionId":"new-session","url":"https://example.test/new","elicitationId":"reused"}`)
	if _, err := h(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	c.cancelSessionAuthorities("old-session")
	_, _ = h(context.Background(), fresh)
	// The wire has no scope/generation, so this delayed completion is ambiguous.
	c.peer.handleNotification(context.Background(), rpcMessage{Method: "elicitation/complete", Params: json.RawMessage(`{"elicitationId":"reused"}`)})
	c.elicitation.deliverCompletion(ElicitationCompletion{ElicitationID: "queue-barrier"}, func() bool { return true })
	select {
	case got := <-delivered:
		if got.ElicitationID != "queue-barrier" {
			t.Fatalf("old completion was rebound to new scope: %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("completion queue barrier did not arrive")
	}
}

func TestAuthorityReviewEmptyEnumCannotBecomeUnrestricted(t *testing.T) {
	for _, raw := range []string{
		`{"type":"string","enum":[]}`,
		`{"type":"string","oneOf":[]}`,
		`{"type":"array","items":{"type":"string","enum":[]}}`,
		`{"type":"array","items":{"anyOf":[]}}`,
	} {
		var property ElicitationProperty
		if err := json.Unmarshal([]byte(raw), &property); err != nil {
			t.Fatal(err)
		}
		var value any = "not-offered"
		if property.Type == "array" {
			value = []string{"not-offered"}
		}
		if err := validateElicitationValue(property, value); err == nil {
			t.Errorf("empty enum became unrestricted: %s", raw)
		}
	}
}

func TestAuthorityReviewClaudeExtraSettingsPreserveDeny(t *testing.T) {
	for _, typ := range []string{ClaudeCodeACPRegistryID, ClaudeCodeNativeRegistryID} {
		agent := Agent{Type: typ}
		_, meta, err := prepareAgentSessionStart(ResolveAgentProfile(agent), StartSessionOptions{Agent: agent, AgentConfig: &AgentConfig{
			Permissions: PermissionConfig{Deny: []string{"Bash(rm:*)"}},
			Extra:       map[string]any{"settings": map[string]any{"env": map[string]any{"FIXTURE": "1"}}},
		}})
		if err != nil {
			t.Fatal(err)
		}
		cc, _ := meta["claudeCode"].(map[string]any)
		options, _ := cc["options"].(map[string]any)
		settings, _ := options["settings"].(map[string]any)
		permissions, _ := settings["permissions"].(map[string]any)
		if got := stringSliceFromAny(permissions["deny"]); len(got) != 1 || got[0] != "Bash(rm:*)" {
			t.Errorf("%s unrelated Extra settings dropped deny: %+v", typ, meta)
		}
	}
}

func TestAuthorityReviewClaudeUnifiedModelReachesStart(t *testing.T) {
	for _, typ := range []string{ClaudeCodeACPRegistryID, ClaudeCodeNativeRegistryID} {
		agent := Agent{Type: typ}
		_, meta, err := prepareAgentSessionStart(ResolveAgentProfile(agent), StartSessionOptions{Agent: agent, AgentConfig: &AgentConfig{Model: "chosen-unified-model"}})
		if err != nil {
			t.Fatal(err)
		}
		got := meta["model"]
		if typ == ClaudeCodeACPRegistryID {
			cc, _ := meta["claudeCode"].(map[string]any)
			options, _ := cc["options"].(map[string]any)
			got = options["model"]
		}
		if got != "chosen-unified-model" {
			t.Errorf("%s unified model lost: %+v", typ, meta)
		}
	}
}
