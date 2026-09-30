package acpruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func stableFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/acp/wire/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func stableHandler(t *testing.T, c *Connection, method string) RPCHandler {
	t.Helper()
	c.peer.handlersMu.RLock()
	defer c.peer.handlersMu.RUnlock()
	h := c.peer.requestHandlers[method]
	if h == nil {
		t.Fatalf("missing handler %s", method)
	}
	return h
}
func stableConnection(t *testing.T, client Client) *Connection {
	t.Helper()
	peer := NewPeer(bytes.NewReader(nil), &syncBuffer{}, PeerOptions{})
	t.Cleanup(peer.Close)
	return NewConnection(peer, client)
}
func equalJSON(t *testing.T, actual any, expected []byte) {
	t.Helper()
	b, err := json.Marshal(actual)
	if err != nil {
		t.Fatal(err)
	}
	var got, want any
	if err = json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(expected, &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("wire=%s want=%s", b, expected)
	}
}

func TestStablePermissionOfficialContextAndOutcome(t *testing.T) {
	var seen PermissionRequest
	c := stableConnection(t, Client{Authority: AuthorityHandlers{Permission: func(_ Context, r PermissionRequest) (PermissionDecision, error) {
		seen = r
		return PermissionDecision{Outcome: "selected", OptionID: "approve-one"}, nil
	}}})
	result, err := stableHandler(t, c, "session/request_permission")(context.Background(), stableFixture(t, "permission-request"))
	if err != nil {
		t.Fatal(err)
	}
	equalJSON(t, result, stableFixture(t, "permission-selected"))
	if seen.ToolCallID != "call-9" || seen.Name == nil || *seen.Name != "write_file" || seen.Status == nil || *seen.Status != "pending" || seen.Kind != "edit" || seen.Title != "Write settings" {
		t.Fatalf("approval context lost: %+v", seen)
	}
	if string(seen.RawInput) != `{"path":"/workspace/settings.json","content":"{}"}` || len(seen.Locations) != 1 || len(seen.Content) != 3 || seen.Content[0].NewText != "{}" || seen.Content[1].Content.Text != "Review change" || seen.Content[2].TerminalID != "terminal-1" {
		t.Fatalf("tool content lost: %+v", seen)
	}
	if seen.Meta["audit"] != "fixture" || seen.ToolCallMeta["origin"] != "fixture" || len(seen.Extra["extension"]) == 0 || len(seen.ToolCallExtra["vendorExtension"]) == 0 {
		t.Fatal("extension context lost")
	}
	equalJSON(t, seen, stableFixture(t, "permission-request"))
}
func TestStablePermissionFailClosed(t *testing.T) {
	cases := []struct {
		name    string
		handler PermissionHandler
		request string
		fixture string
	}{
		{name: "no handler", fixture: "permission-rejected"},
		{name: "handler error", handler: func(Context, PermissionRequest) (PermissionDecision, error) {
			return PermissionDecision{Outcome: "selected", OptionID: "approve-one"}, errors.New("host error")
		}, fixture: "permission-rejected"},
		{name: "unknown option", handler: func(Context, PermissionRequest) (PermissionDecision, error) {
			return PermissionDecision{Outcome: "selected", OptionID: "not-offered"}, nil
		}, fixture: "permission-cancelled"},
		{name: "unknown outcome", handler: func(Context, PermissionRequest) (PermissionDecision, error) {
			return PermissionDecision{Outcome: "allow", OptionID: "approve-one"}, nil
		}, fixture: "permission-cancelled"},
		{name: "no reject", request: `{"sessionId":"s","toolCall":{"toolCallId":"t"},"options":[{"optionId":"allow","name":"Allow","kind":"allow_once"}]}`, fixture: "permission-cancelled"},
		{name: "unknown reject prefix", request: `{"sessionId":"s","toolCall":{"toolCallId":"t"},"options":[{"optionId":"x","name":"Reject maybe","kind":"reject_future"}]}`, fixture: "permission-cancelled"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := stableConnection(t, Client{Authority: AuthorityHandlers{Permission: tc.handler}})
			raw := stableFixture(t, "permission-request")
			if tc.request != "" {
				raw = []byte(tc.request)
			}
			result, err := stableHandler(t, c, "session/request_permission")(context.Background(), raw)
			if err != nil {
				t.Fatal(err)
			}
			equalJSON(t, result, stableFixture(t, tc.fixture))
		})
	}
}
func TestStablePermissionMalformedNeverCallsAuthority(t *testing.T) {
	for _, raw := range []string{`null`, `{}`, `{"sessionId":"s","toolCallId":"t","options":[]}`, `{"sessionId":"s","toolCall":{"toolCallId":"t"},"options":null}`, `{"sessionId":"s","toolCall":{"toolCallId":"t"},"options":[{"id":"a","name":"Allow","kind":"allow_once"}]}`, `{"sessionId":"s","toolCall":{"toolCallId":"t"},"options":[{"optionId":"a","name":"Allow","kind":"allow_once"},{"optionId":"a","name":"Reject","kind":"reject_once"}]}`} {
		c := stableConnection(t, Client{Authority: AuthorityHandlers{Permission: func(Context, PermissionRequest) (PermissionDecision, error) {
			t.Fatal("malformed permission reached authority")
			return PermissionDecision{}, nil
		}}})
		_, err := stableHandler(t, c, "session/request_permission")(context.Background(), []byte(raw))
		var rpcErr *RPCError
		if !errors.As(err, &rpcErr) || rpcErr.Code != -32602 {
			t.Fatalf("raw %s error=%v", raw, err)
		}
	}
}
func TestStablePermissionTimeoutCancelAndLateLease(t *testing.T) {
	for _, mode := range []string{"timeout", "session cancel", "peer close", "late lease"} {
		t.Run(mode, func(t *testing.T) {
			started := make(chan struct{})
			release := make(chan struct{})
			var valid atomic.Bool
			valid.Store(true)
			c := stableConnection(t, Client{Authority: AuthorityHandlers{PermissionTimeout: 30 * time.Millisecond, Permission: func(ctx Context, _ PermissionRequest) (PermissionDecision, error) {
				close(started)
				select {
				case <-ctx.Done():
				case <-release:
				}
				return PermissionDecision{Outcome: "selected", OptionID: "approve-one"}, nil
			}}})
			c.SetPermissionLease(func(PermissionRequest) func() bool { return valid.Load })
			done := make(chan any, 1)
			go func() {
				r, _ := stableHandler(t, c, "session/request_permission")(context.Background(), stableFixture(t, "permission-request"))
				done <- r
			}()
			<-started
			switch mode {
			case "session cancel":
				c.cancelSessionAuthorities("session-42")
			case "peer close":
				c.peer.Close()
			case "late lease":
				valid.Store(false)
				close(release)
			}
			select {
			case r := <-done:
				equalJSON(t, r, stableFixture(t, "permission-cancelled"))
			case <-time.After(time.Second):
				t.Fatal("approval did not cancel")
			}
		})
	}
}
func TestStablePermissionOutcomeRejectsFlatAndUnknown(t *testing.T) {
	for _, raw := range []string{`{"outcome":"selected","optionId":"x"}`, `{"outcome":{"outcome":"allow"}}`, `{"outcome":{"outcome":"selected"}}`, `{}`} {
		var d PermissionDecision
		if json.Unmarshal([]byte(raw), &d) == nil {
			t.Fatalf("invalid outcome accepted: %s", raw)
		}
	}
}
func TestStableBooleanAndUsagePresence(t *testing.T) {
	var option SessionConfigOption
	if err := json.Unmarshal(stableFixture(t, "boolean-option"), &option); err != nil {
		t.Fatal(err)
	}
	if v, ok := option.Value.(bool); !ok || v {
		t.Fatalf("false not preserved: %#v", option.Value)
	}
	equalJSON(t, option, stableFixture(t, "boolean-option"))
	var req SetSessionConfigOptionRequest
	if err := json.Unmarshal(stableFixture(t, "boolean-set"), &req); err != nil {
		t.Fatal(err)
	}
	equalJSON(t, req, stableFixture(t, "boolean-set"))
	for _, v := range []string{"null", "0", `"false"`} {
		var out SessionConfigOption
		if json.Unmarshal([]byte(`{"id":"x","name":"X","type":"boolean","currentValue":`+v+`}`), &out) == nil {
			t.Fatalf("boolean accepted %s", v)
		}
	}
	for _, raw := range []string{`{"id":"x","name":"X","type":"boolean"}`, `{"sessionId":"s","configId":"x","type":"boolean"}`, `{"sessionId":"s","configId":"x","value":false}`} {
		var err error
		if strings.Contains(raw, "currentValue") || strings.Contains(raw, `"name"`) {
			var o SessionConfigOption
			err = json.Unmarshal([]byte(raw), &o)
		} else {
			var r SetSessionConfigOptionRequest
			err = json.Unmarshal([]byte(raw), &r)
		}
		if err == nil {
			t.Fatalf("invalid bool accepted %s", raw)
		}
	}
	var notification SessionNotification
	if err := json.Unmarshal(stableFixture(t, "usage-update"), &notification); err != nil {
		t.Fatal(err)
	}
	if notification.Update.Used == nil || *notification.Update.Used != 0 || *notification.Update.Size != 200000 || notification.Update.Cost.Amount != 0.125 {
		t.Fatalf("bad usage: %+v", notification.Update)
	}
	equalJSON(t, notification, stableFixture(t, "usage-update"))
	for _, raw := range []string{`{"sessionUpdate":"usage_update","usage":{"inputTokens":3}}`, `{"sessionUpdate":"usage_update","used":null,"size":1}`, `{"sessionUpdate":"usage_update","used":-1,"size":1}`} {
		var u SessionUpdate
		if json.Unmarshal([]byte(raw), &u) == nil {
			t.Fatalf("invalid usage accepted %s", raw)
		}
	}
}
func TestStableCapabilitiesPreservePresentEmpty(t *testing.T) {
	for _, raw := range []string{`{"list":{},"resume":{},"close":{},"delete":{},"additionalDirectories":{}}`, `{}`} {
		var c SessionCapabilities
		if err := json.Unmarshal([]byte(raw), &c); err != nil {
			t.Fatal(err)
		}
		equalJSON(t, c, []byte(raw))
	}
	var c SessionCapabilities
	if err := json.Unmarshal([]byte(`{"list":null}`), &c); err != nil || c.List != nil {
		t.Fatalf("null advertised: %+v %v", c, err)
	}
	if json.Unmarshal([]byte(`{"list":false}`), &c) == nil {
		t.Fatal("false accepted as capability object")
	}
	client := defaultClient(RuntimeOptions{}, AuthorityHandlers{})
	b, _ := json.Marshal(client.Capabilities)
	if bytes.Contains(b, []byte(`"elicitation"`)) || bytes.Contains(b, []byte(`"auth"`)) {
		t.Fatalf("unsupported capability: %s", b)
	}
	if !bytes.Contains(b, []byte(`"boolean":{}`)) {
		t.Fatalf("boolean presence lost: %s", b)
	}
}
func TestStableToolNameNullAndOmitted(t *testing.T) {
	for _, raw := range []string{`{"sessionUpdate":"tool_call_update","toolCallId":"t"}`, `{"sessionUpdate":"tool_call_update","toolCallId":"t","name":null}`} {
		var u SessionUpdate
		if err := json.Unmarshal([]byte(raw), &u); err != nil || u.Name != nil {
			t.Fatalf("name presence incorrect: %+v %v", u, err)
		}
	}
}
func TestStableUnadvertisedMethodsNeverWrite(t *testing.T) {
	c := stableConnection(t, Client{})
	c.initialized = true
	for _, method := range []string{"session/load", "session/resume", "session/list", "session/close", "session/delete", "logout"} {
		if err := c.requireCapability(method); err == nil {
			t.Fatalf("missing capability allowed %s", method)
		}
	}
	if len(c.peer.writer.(*syncBuffer).Bytes()) != 0 {
		t.Fatal("unexpected outbound frame")
	}
	if _, err := c.ForkSession(context.Background(), ForkSessionRequest{}); err == nil {
		t.Fatal("experimental fork allowed")
	}
}
func TestStableBooleanRequiresAdvertisedOption(t *testing.T) {
	c := stableConnection(t, Client{})
	_, err := c.SetSessionConfigOption(context.Background(), SetSessionConfigOptionRequest{SessionID: "s", OptionID: "x", Value: false})
	if err == nil || !strings.Contains(err.Error(), "not advertised") {
		t.Fatalf("error=%v", err)
	}
	if len(c.peer.writer.(*syncBuffer).Bytes()) != 0 {
		t.Fatal("unadvertised bool sent")
	}
}

func TestStableGroupedSelectRoundTrip(t *testing.T) {
	raw := []byte(`{"id":"model","name":"Model","type":"select","currentValue":"one","options":[{"group":"models","name":"Models","options":[{"value":"one","name":"One","_meta":{"vendor":"x"}}]}]}`)
	var option SessionConfigOption
	if err := json.Unmarshal(raw, &option); err != nil {
		t.Fatal(err)
	}
	if len(option.Groups) != 1 || option.Groups[0].ID != "models" || option.Groups[0].Options[0].Value != "one" {
		t.Fatalf("grouped options lost: %+v", option)
	}
	equalJSON(t, option, raw)
}
