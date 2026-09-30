package acpruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// Unit callback tests use a session scope. The fixed request-scoped golden is
// retained unchanged for independent wire and real outgoing-request tests.
func stableSessionFormFixture(t *testing.T) []byte {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(stableFixture(t, "elicitation-form"), &fields); err != nil {
		t.Fatal(err)
	}
	delete(fields, "requestId")
	fields["sessionId"] = json.RawMessage(`"session-42"`)
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestStableElicitationCapabilitiesAndModes(t *testing.T) {
	for _, tc := range []struct {
		name string
		h    ElicitationHandlers
		want string
	}{
		{"none", ElicitationHandlers{}, `null`},
		{"form", ElicitationHandlers{Form: func(Context, ElicitationRequest) (ElicitationResponse, error) {
			return ElicitationResponse{Action: "decline"}, nil
		}}, `{"form":{}}`},
		{"url", ElicitationHandlers{URL: func(Context, ElicitationRequest) (ElicitationResponse, error) {
			return ElicitationResponse{Action: "cancel"}, nil
		}}, `{"url":{}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			equalJSON(t, elicitationCapabilities(tc.h), []byte(tc.want))
			c := stableConnection(t, Client{Authority: AuthorityHandlers{Elicitation: tc.h}})
			for _, name := range []string{"elicitation-form", "elicitation-url"} {
				raw := stableFixture(t, name)
				if name == "elicitation-form" {
					raw = stableSessionFormFixture(t)
				}
				result, err := stableHandler(t, c, "elicitation/create")(context.Background(), raw)
				supported := name == "elicitation-form" && tc.h.Form != nil || name == "elicitation-url" && tc.h.URL != nil
				if supported {
					if err != nil {
						t.Fatal(err)
					}
					if result == nil {
						t.Fatal("no elicitation response")
					}
				} else {
					var rpcErr *RPCError
					if !errors.As(err, &rpcErr) || rpcErr.Code != -32602 {
						t.Fatalf("unadvertised mode error=%v", err)
					}
				}
			}
		})
	}
}
func TestStableElicitationRejectsUnknownMalformedAndCredentials(t *testing.T) {
	var calls atomic.Int32
	h := func(Context, ElicitationRequest) (ElicitationResponse, error) {
		calls.Add(1)
		return ElicitationResponse{Action: "accept"}, nil
	}
	c := stableConnection(t, Client{Authority: AuthorityHandlers{Elicitation: ElicitationHandlers{Form: h, URL: h}}})
	for _, raw := range []string{
		`{}`, `{"message":"Question","sessionId":"s","mode":"future"}`, `{"message":"Question","mode":"form","requestedSchema":{}}`,
		`{"message":"Question","sessionId":"s","requestId":1,"mode":"form","requestedSchema":{}}`,
		`{"message":"Question","requestId":1.5,"mode":"form","requestedSchema":{}}`,
		`{"message":"Question","sessionId":"s","mode":"form","requestedSchema":{"properties":{"password":{"type":"string"}}}}`,
		`{"message":"Question","sessionId":"s","mode":"form","requestedSchema":{"properties":{"key":{"type":"string","title":"API key"}}}}`,
		`{"message":"Question","sessionId":"s","mode":"form","requestedSchema":{"properties":{"field":{"type":"object"}}}}`,
		`{"message":"Question","sessionId":"s","mode":"url","elicitationId":"e","url":"javascript:alert(1)"}`,
		`{"message":"Question","sessionId":"s","mode":"url","elicitationId":"e","url":"https://user:password@example.com"}`,
	} {
		_, err := stableHandler(t, c, "elicitation/create")(context.Background(), []byte(raw))
		var rpcErr *RPCError
		if !errors.As(err, &rpcErr) || rpcErr.Code != -32602 {
			t.Fatalf("invalid request accepted %s: %v", raw, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid request invoked UI")
	}
}
func TestStableElicitationResponseValidationAndFalse(t *testing.T) {
	cases := []struct {
		name     string
		response ElicitationResponse
		want     string
	}{
		{"false preserved", ElicitationResponse{Action: "accept", Content: map[string]any{"color": "blue", "enabled": false}}, `{"action":"accept","content":{"color":"blue","enabled":false}}`},
		{"decline drops content", ElicitationResponse{Action: "decline", Content: map[string]any{"unexpected": "value"}}, `{"action":"decline"}`},
		{"cancel drops content", ElicitationResponse{Action: "cancel", Content: map[string]any{"unexpected": "value"}}, `{"action":"cancel"}`},
		{"unknown action", ElicitationResponse{Action: "unknown"}, `{"action":"cancel"}`},
		{"wrong bool", ElicitationResponse{Action: "accept", Content: map[string]any{"color": "blue", "enabled": "false"}}, `{"action":"cancel"}`},
		{"missing required", ElicitationResponse{Action: "accept", Content: map[string]any{"color": "blue"}}, `{"action":"cancel"}`},
		{"wrong enum", ElicitationResponse{Action: "accept", Content: map[string]any{"color": "red", "enabled": false}}, `{"action":"cancel"}`},
		{"unknown content", ElicitationResponse{Action: "accept", Content: map[string]any{"color": "blue", "enabled": false, "extra": "not requested"}}, `{"action":"cancel"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := stableConnection(t, Client{Authority: AuthorityHandlers{Elicitation: ElicitationHandlers{Form: func(Context, ElicitationRequest) (ElicitationResponse, error) { return tc.response, nil }}}})
			r, err := stableHandler(t, c, "elicitation/create")(context.Background(), stableSessionFormFixture(t))
			if err != nil {
				t.Fatal(err)
			}
			equalJSON(t, r, []byte(tc.want))
		})
	}
}
func TestStableElicitationURLCompletionBoundToConnectionAndLease(t *testing.T) {
	var calls atomic.Int32
	delivered := make(chan struct{}, 4)
	var live atomic.Bool
	live.Store(true)
	c := stableConnection(t, Client{Authority: AuthorityHandlers{Elicitation: ElicitationHandlers{URL: func(Context, ElicitationRequest) (ElicitationResponse, error) {
		return ElicitationResponse{Action: "accept"}, nil
	}, OnComplete: func(_ Context, event ElicitationCompletion) {
		if event.Request.SessionID != "session-42" || event.Request.ToolCallID == nil || *event.Request.ToolCallID != "call-9" {
			t.Error("completion lost scope")
		}
		calls.Add(1)
		delivered <- struct{}{}
	}}}})
	c.SetElicitationLease(func(ElicitationRequest) func() bool { return live.Load })
	notify := func(id string) {
		c.peer.handleNotification(context.Background(), rpcMessage{Method: "elicitation/complete", Params: json.RawMessage(`{"elicitationId":"` + id + `"}`)})
	}
	notify("unknown")
	if calls.Load() != 0 {
		t.Fatal("unknown completion delivered")
	}
	raw := stableFixture(t, "elicitation-url")
	h := stableHandler(t, c, "elicitation/create")
	if _, err := h(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	if _, err := h(context.Background(), raw); err == nil {
		t.Fatal("duplicate outstanding URL ID accepted")
	}
	notify("url-1")
	select {
	case <-delivered:
	case <-time.After(time.Second):
		t.Fatal("completion callback not delivered")
	}
	notify("url-1")
	if calls.Load() != 1 {
		t.Fatalf("completion count=%d", calls.Load())
	}
	if _, err := h(context.Background(), raw); err == nil {
		t.Fatal("completed URL ID was reused")
	}
	fresh := bytes.ReplaceAll(raw, []byte(`"url-1"`), []byte(`"url-2"`))
	if _, err := h(context.Background(), fresh); err != nil {
		t.Fatal(err)
	}
	live.Store(false)
	notify("url-2")
	if calls.Load() != 1 {
		t.Fatal("late completion escaped old lease")
	}
}
func TestStableElicitationCancelledScopeDoesNotComplete(t *testing.T) {
	var calls atomic.Int32
	c := stableConnection(t, Client{Authority: AuthorityHandlers{Elicitation: ElicitationHandlers{URL: func(Context, ElicitationRequest) (ElicitationResponse, error) {
		return ElicitationResponse{Action: "accept"}, nil
	}, OnComplete: func(Context, ElicitationCompletion) { calls.Add(1) }}}})
	if _, err := stableHandler(t, c, "elicitation/create")(context.Background(), stableFixture(t, "elicitation-url")); err != nil {
		t.Fatal(err)
	}
	c.cancelSessionAuthorities("session-42")
	c.peer.handleNotification(context.Background(), rpcMessage{Method: "elicitation/complete", Params: stableFixture(t, "elicitation-complete")})
	if calls.Load() != 0 {
		t.Fatal("cancelled URL completed")
	}
}
func TestStableElicitationTimeoutAndCloseCancel(t *testing.T) {
	for _, closePeer := range []bool{false, true} {
		t.Run(map[bool]string{false: "timeout", true: "close"}[closePeer], func(t *testing.T) {
			started := make(chan struct{})
			c := stableConnection(t, Client{Authority: AuthorityHandlers{Elicitation: ElicitationHandlers{Timeout: 30 * time.Millisecond, Form: func(ctx Context, _ ElicitationRequest) (ElicitationResponse, error) {
				close(started)
				<-ctx.Done()
				return ElicitationResponse{Action: "accept", Content: map[string]any{"color": "blue", "enabled": false}}, nil
			}}}})
			result := make(chan any, 1)
			go func() {
				r, _ := stableHandler(t, c, "elicitation/create")(context.Background(), stableSessionFormFixture(t))
				result <- r
			}()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("elicitation callback did not start")
			}
			if closePeer {
				c.peer.Close()
			}
			select {
			case r := <-result:
				equalJSON(t, r, []byte(`{"action":"cancel"}`))
			case <-time.After(time.Second):
				t.Fatal("elicitation blocked after cancellation")
			}
		})
	}
}

func TestStableElicitationNumericNullRejected(t *testing.T) {
	for _, kind := range []string{"number", "integer"} {
		if err := validateElicitationValue(ElicitationProperty{Type: kind}, nil); err == nil {
			t.Fatalf("%s accepted null", kind)
		}
	}
}

func TestStableBlockedElicitationCompletionDoesNotBlockProtocol(t *testing.T) {
	started := make(chan struct{})
	exited := make(chan struct{})
	c := stableConnection(t, Client{Authority: AuthorityHandlers{Elicitation: ElicitationHandlers{URL: func(Context, ElicitationRequest) (ElicitationResponse, error) {
		return ElicitationResponse{Action: "accept"}, nil
	}, OnComplete: func(ctx Context, _ ElicitationCompletion) { close(started); <-ctx.Done(); close(exited) }}}})
	if _, err := stableHandler(t, c, "elicitation/create")(context.Background(), stableFixture(t, "elicitation-url")); err != nil {
		t.Fatal(err)
	}
	notified := make(chan struct{})
	go func() {
		c.peer.handleNotification(context.Background(), rpcMessage{Method: "elicitation/complete", Params: stableFixture(t, "elicitation-complete")})
		close(notified)
	}()
	select {
	case <-notified:
	case <-time.After(time.Second):
		t.Fatal("notification reader blocked on host callback")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("completion callback did not start")
	}
	c.cancelSessionAuthorities("session-42")
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("completion callback context not cancelled on session cancel")
	}
}

func TestStableElicitationCompletionQueueHasBoundedDropCount(t *testing.T) {
	started := make(chan struct{})
	var once atomic.Bool
	c := stableConnection(t, Client{Authority: AuthorityHandlers{Elicitation: ElicitationHandlers{URL: func(Context, ElicitationRequest) (ElicitationResponse, error) {
		return ElicitationResponse{Action: "accept"}, nil
	}, OnComplete: func(ctx Context, _ ElicitationCompletion) {
		if once.CompareAndSwap(false, true) {
			close(started)
		}
		<-ctx.Done()
	}}}})
	event := ElicitationCompletion{ElicitationID: "one", Request: ElicitationRequest{SessionID: "s"}}
	c.elicitation.deliverCompletion(event, func() bool { return true })
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("completion callback did not start")
	}
	for i := 0; i < maxConcurrentAuthorityCalls+1; i++ {
		c.elicitation.deliverCompletion(event, func() bool { return true })
	}
	if got := c.ElicitationCompletionDrops(); got != 1 {
		t.Fatalf("completion queue drops=%d want1", got)
	}
	c.peer.Close()
}
