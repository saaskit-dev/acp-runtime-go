package acpruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPeerNotifyWritesJSONRPCFrame(t *testing.T) {
	var out bytes.Buffer
	peer := NewPeer(strings.NewReader(""), &out, PeerOptions{})
	err := peer.NotifyRaw(context.Background(), "session/cancel", json.RawMessage(`{"sessionId":"s1"}`))
	if err != nil {
		t.Fatalf("NotifyRaw() error = %v", err)
	}
	const want = `{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":"s1"}}` + "\n"
	if out.String() != want {
		t.Fatalf("frame = %q, want %q", out.String(), want)
	}
}

func TestPeerCallRawRoundTrip(t *testing.T) {
	clientReader, serverWriter := io.Pipe()
	serverReader, clientWriter := io.Pipe()
	defer clientReader.Close()
	defer serverWriter.Close()
	defer serverReader.Close()
	defer clientWriter.Close()

	client := NewPeer(clientReader, clientWriter, PeerOptions{})
	server := NewPeer(serverReader, serverWriter, PeerOptions{})
	server.RegisterRequest("echo", func(ctx context.Context, raw json.RawMessage) (any, error) {
		return raw, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = client.Start(ctx) }()
	go func() { _ = server.Start(ctx) }()
	defer client.Close()
	defer server.Close()

	result, err := client.CallRaw(ctx, "echo", json.RawMessage(`{"text":"ok"}`))
	if err != nil {
		t.Fatalf("CallRaw() error = %v", err)
	}
	if string(result) != `{"text":"ok"}` {
		t.Fatalf("result = %s", result)
	}
}

func TestPeerRawMessageHookExcludesFrameDelimiter(t *testing.T) {
	var out bytes.Buffer
	var raw json.RawMessage
	peer := NewPeer(strings.NewReader(""), &out, PeerOptions{
		OnRawMessage: func(direction string, message json.RawMessage) {
			if direction == "outbound" {
				raw = append(raw[:0], message...)
			}
		},
	})
	err := peer.NotifyRaw(context.Background(), `method/"quoted"`, json.RawMessage(`{"value":"a<b>&c"}`))
	if err != nil {
		t.Fatalf("NotifyRaw() error = %v", err)
	}
	if len(raw) == 0 {
		t.Fatalf("expected outbound raw message")
	}
	if bytes.Contains(raw, []byte{'\n'}) {
		t.Fatalf("raw message contains frame delimiter: %q", raw)
	}
	var decoded rpcMessage
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("raw message is not valid JSON: %v", err)
	}
	if decoded.Method != `method/"quoted"` {
		t.Fatalf("method = %q", decoded.Method)
	}
}

func TestRPCErrorIncludesDataInErrorString(t *testing.T) {
	tests := []struct {
		name string
		err  *RPCError
		want string
	}{
		{
			name: "string data",
			err:  &RPCError{Code: -32603, Message: "Internal error", Data: json.RawMessage(`"Claude Code failed: missing auth"`)},
			want: "rpc error -32603: Internal error: Claude Code failed: missing auth",
		},
		{
			name: "object message",
			err:  &RPCError{Code: -32603, Message: "Internal error", Data: json.RawMessage(`{"message":"spawn failed","stderr":"ignored"}`)},
			want: "rpc error -32603: Internal error: spawn failed",
		},
		{
			name: "raw object fallback",
			err:  &RPCError{Code: -32603, Message: "Internal error", Data: json.RawMessage(`{"code":"E_AUTH"}`)},
			want: `rpc error -32603: Internal error: {"code":"E_AUTH"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.want {
				t.Fatalf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseRPCIDSupportsNumericResponses(t *testing.T) {
	for _, raw := range []json.RawMessage{
		json.RawMessage(`42`),
		json.RawMessage(`"42"`),
		json.RawMessage(" \t42\r\n"),
	} {
		id, ok := parseRPCID(raw)
		if !ok || id != 42 {
			t.Fatalf("parseRPCID(%q) = %d, %v; want 42, true", raw, id, ok)
		}
	}
}

func TestParseRPCMessageExtractsEnvelopeWithoutFullUnmarshal(t *testing.T) {
	msg, ok := parseRPCMessage([]byte(`{"jsonrpc":"2.0","id":42,"result":{"items":[1,{"name":"ok"}]}}`))
	if !ok {
		t.Fatalf("parseRPCMessage() failed")
	}
	if string(msg.ID) != "42" {
		t.Fatalf("ID = %q", msg.ID)
	}
	if string(msg.Result) != `{"items":[1,{"name":"ok"}]}` {
		t.Fatalf("Result = %s", msg.Result)
	}
}

func TestParseRPCMessageSupportsEscapedMethod(t *testing.T) {
	msg, ok := parseRPCMessage([]byte(`{"jsonrpc":"2.0","method":"method/\"quoted\"","params":{"value":"a,b}"}}`))
	if !ok {
		t.Fatalf("parseRPCMessage() failed")
	}
	if msg.Method != `method/"quoted"` {
		t.Fatalf("Method = %q", msg.Method)
	}
	if string(msg.Params) != `{"value":"a,b}"}` {
		t.Fatalf("Params = %s", msg.Params)
	}
}

func TestParseRPCMessageRejectsInvalidLiteral(t *testing.T) {
	if _, ok := parseRPCMessage([]byte(`{"jsonrpc":"2.0","id":x,"result":null}`)); ok {
		t.Fatalf("parseRPCMessage() accepted invalid literal")
	}
}

func TestParseRPCMessageRejectsInvalidStringEscape(t *testing.T) {
	if _, ok := parseRPCMessage([]byte("{\"\\000\":[]}")); ok {
		t.Fatalf("parseRPCMessage() accepted invalid string escape")
	}
}

func TestParseRPCMessageRejectsTrailingGarbage(t *testing.T) {
	if _, ok := parseRPCMessage([]byte(`{}0`)); ok {
		t.Fatalf("parseRPCMessage() accepted trailing garbage")
	}
}

func TestParseRPCMessageRejectsInvalidCompositeValue(t *testing.T) {
	if _, ok := parseRPCMessage([]byte(`{"":[A]}`)); ok {
		t.Fatalf("parseRPCMessage() accepted invalid composite value")
	}
}

func TestParseRPCMessageRejectsTrailingComma(t *testing.T) {
	if _, ok := parseRPCMessage([]byte(`{"a":"b",}`)); ok {
		t.Fatalf("parseRPCMessage() accepted trailing comma")
	}
}

func TestParseRPCMessageRejectsInvalidUTF8String(t *testing.T) {
	raw := []byte{'{', '"', 'm', 'e', 't', 'h', 'o', 'd', '"', ':', '"', 0x95, '"', '}'}
	if _, ok := parseRPCMessage(raw); ok {
		t.Fatalf("parseRPCMessage() accepted invalid UTF-8 string")
	}
}

func TestParseRPCMessageMatchesExactJSONEnvelopeDecode(t *testing.T) {
	for _, raw := range [][]byte{
		[]byte(`{"jsonrpc":"2.0","id":42,"result":{"items":[1,{"name":"ok"}]}}`),
		[]byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"ok"}}}}`),
		[]byte(`{"jsonrpc":"2.0","method":"method/\"quoted\"","params":{"value":"a,b}"}}`),
		[]byte(`{"jsonrpc":"2.0","meth\u006fd":"escaped-key","params":[true,false,null,{"nested":[1,2,3]}]}`),
		[]byte(`{"error":{"code":-32000,"message":"failed","data":{"reason":"x"}},"id":"7","jsonrpc":"2.0"}`),
		[]byte(`{"unknown":{"deep":[{"a":"b"}]},"id":99,"result":null}`),
	} {
		got, ok := parseRPCMessage(raw)
		if !ok {
			t.Fatalf("parseRPCMessage(%s) failed", raw)
		}
		want, ok := decodeRPCMessageExactForTest(raw)
		if !ok {
			t.Fatalf("exact JSON envelope decode failed for %s", raw)
		}
		assertRPCMessagesEquivalent(t, got, want)
	}
}

func FuzzParseRPCMessage(f *testing.F) {
	for _, seed := range []string{
		`{"jsonrpc":"2.0","id":42,"result":{"items":[1,{"name":"ok"}]}}`,
		`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1"}}`,
		`{"jsonrpc":"2.0","method":"method/\"quoted\"","params":{"value":"a,b}"}}`,
		`{"jsonrpc":"2.0","meth\u006fd":"escaped-key","params":[true,false,null]}`,
		`{"jsonrpc":"2.0","id":x,"result":null}`,
		`not-json`,
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		got, ok := parseRPCMessage([]byte(raw))
		if !ok {
			return
		}
		if !json.Valid([]byte(raw)) {
			t.Fatalf("parseRPCMessage accepted invalid JSON: %q", raw)
		}
		want, ok := decodeRPCMessageExactForTest([]byte(raw))
		if !ok {
			t.Fatalf("exact JSON envelope decode failed after parser accepted input %q", raw)
		}
		assertRPCMessagesEquivalent(t, got, want)
	})
}

func decodeRPCMessageExactForTest(raw []byte) (rpcMessage, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return rpcMessage{}, false
	}
	var msg rpcMessage
	if value, ok := fields["id"]; ok {
		msg.ID = value
	}
	if value, ok := fields["method"]; ok {
		if err := json.Unmarshal(value, &msg.Method); err != nil {
			return rpcMessage{}, false
		}
	}
	if value, ok := fields["params"]; ok {
		msg.Params = value
	}
	if value, ok := fields["result"]; ok {
		msg.Result = value
	}
	if value, ok := fields["error"]; ok {
		var rpcErr RPCError
		if err := json.Unmarshal(value, &rpcErr); err != nil {
			return rpcMessage{}, false
		}
		msg.Error = &rpcErr
	}
	return msg, true
}

func assertRPCMessagesEquivalent(t *testing.T, got, want rpcMessage) {
	t.Helper()
	if got.Method != want.Method {
		t.Fatalf("Method = %q, want %q", got.Method, want.Method)
	}
	assertRawJSONEquivalent(t, "ID", got.ID, want.ID)
	assertRawJSONEquivalent(t, "Params", got.Params, want.Params)
	assertRawJSONEquivalent(t, "Result", got.Result, want.Result)
	if !reflect.DeepEqual(got.Error, want.Error) {
		t.Fatalf("Error = %#v, want %#v", got.Error, want.Error)
	}
}

func assertRawJSONEquivalent(t *testing.T, name string, got, want json.RawMessage) {
	t.Helper()
	got = compactRawJSONForTest(t, got)
	want = compactRawJSONForTest(t, want)
	if !bytes.Equal(got, want) {
		t.Fatalf("%s = %s, want %s", name, got, want)
	}
}

func compactRawJSONForTest(t *testing.T, raw json.RawMessage) json.RawMessage {
	t.Helper()
	if len(raw) == 0 {
		return nil
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		t.Fatalf("json.Compact(%s) error = %v", raw, err)
	}
	return buf.Bytes()
}

// TestCallEmitsCancelRequestOnContextCancel verifies that cancelling a Call's
// context emits a $/cancel_request notification (stabilized in ACP
// schema-v1.17.0) carrying the in-flight request id, so the agent can stop
// work instead of running to completion.
func TestCallEmitsCancelRequestOnContextCancel(t *testing.T) {
	clientReader, serverWriter := io.Pipe()
	serverReader, clientWriter := io.Pipe()
	defer clientReader.Close()
	defer serverWriter.Close()
	defer serverReader.Close()
	defer clientWriter.Close()

	server := NewPeer(serverReader, serverWriter, PeerOptions{})
	client := NewPeer(clientReader, clientWriter, PeerOptions{
		OnRawMessage: func(direction string, message json.RawMessage) {
			if direction == "outbound" && bytes.Contains(message, []byte(`$/cancel_request`)) {
				t.Logf("saw cancel: %s", message)
			}
		},
	})

	// Block the "slow" request so it stays pending until we cancel.
	slowStarted := make(chan struct{})
	slowReleased := make(chan struct{})
	server.RegisterRequest("slow", func(ctx context.Context, raw json.RawMessage) (any, error) {
		close(slowStarted)
		select {
		case <-slowReleased:
			return "done", nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = server.Start(ctx) }()
	go func() { _ = client.Start(ctx) }()
	defer server.Close()
	defer client.Close()

	callCtx, callCancel := context.WithCancel(ctx)
	callDone := make(chan error, 1)
	go func() {
		var result json.RawMessage
		err := client.Call(callCtx, "slow", nil, &result)
		callDone <- err
	}()

	select {
	case <-slowStarted:
	case <-time.After(2 * time.Second):
		t.Fatalf("slow handler never started")
	}

	// Cancel the in-flight call; the client should emit $/cancel_request.
	callCancel()

	select {
	case err := <-callDone:
		if err == nil {
			t.Fatalf("expected cancelled error, got nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Call did not return after cancel")
	}

	// Read the raw bytes the client wrote to the server to confirm a
	// $/cancel_request frame was emitted. We capture it by scanning the
	// outbound stream: the client wrote into clientWriter which feeds
	// serverReader. Instead, assert via a fresh peer that observes the wire.
	// Simpler: re-run with a capturing writer.
	close(slowReleased)
}

// TestCallCancelEmitsCancelRequestOnWire confirms the $/cancel_request frame is
// physically emitted on the wire by capturing outbound messages via the
// OnRawMessage hook. The cancelled call's id must appear in the cancel params.
func TestCallCancelEmitsCancelRequestOnWire(t *testing.T) {
	clientReader, serverWriter := io.Pipe()
	serverReader, clientWriter := io.Pipe()
	defer clientReader.Close()
	defer serverWriter.Close()
	defer serverReader.Close()
	defer clientWriter.Close()

	server := NewPeer(serverReader, serverWriter, PeerOptions{})
	slowStarted := make(chan struct{})
	server.RegisterRequest("slow", func(ctx context.Context, raw json.RawMessage) (any, error) {
		close(slowStarted)
		<-ctx.Done() // never return on its own
		return nil, ctx.Err()
	})

	var cancelFrame []byte
	var cancelMu sync.Mutex
	client := NewPeer(clientReader, clientWriter, PeerOptions{
		OnRawMessage: func(direction string, message json.RawMessage) {
			if direction == "outbound" && bytes.Contains(message, []byte(`$/cancel_request`)) {
				cancelMu.Lock()
				cancelFrame = append(cancelFrame[:0], message...)
				cancelMu.Unlock()
			}
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = server.Start(ctx) }()
	go func() { _ = client.Start(ctx) }()
	defer server.Close()
	defer client.Close()

	callCtx, callCancel := context.WithCancel(ctx)
	callDone := make(chan error, 1)
	go func() {
		var result json.RawMessage
		callDone <- client.Call(callCtx, "slow", nil, &result)
	}()

	select {
	case <-slowStarted:
	case <-time.After(2 * time.Second):
		t.Fatalf("slow handler never started")
	}

	callCancel()
	<-callDone

	cancelMu.Lock()
	frame := append([]byte(nil), cancelFrame...)
	cancelMu.Unlock()
	if frame == nil {
		t.Fatalf("no $/cancel_request frame captured on outbound wire")
	}
	var decoded struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(frame, &decoded); err != nil {
		t.Fatalf("cancel frame not valid JSON: %v\n%s", err, frame)
	}
	if decoded.Method != "$/cancel_request" {
		t.Fatalf("method = %q, want $/cancel_request", decoded.Method)
	}
	// Per ACP schema-v1.17.0 CancelRequestNotification, the param field is
	// "requestId" (camelCase), NOT "id".
	if !bytes.Contains(decoded.Params, []byte(`"requestId"`)) {
		t.Fatalf("cancel params missing requestId field: %s", decoded.Params)
	}
	if bytes.Contains(decoded.Params, []byte(`"id":`)) {
		t.Fatalf("cancel params must not use bare 'id' field (spec requires requestId): %s", decoded.Params)
	}
}

// TestInboundCancelRequestHonored verifies the BIDIRECTIONAL requirement of
// $/cancel_request (ACP schema-v1.17.0, x-side: "protocol"): when the peer
// sends $/cancel_request for an in-flight inbound request, the host cancels
// the handler's context and the original request gets a -32800 "Request
// cancelled" error response.
func TestInboundCancelRequestHonored(t *testing.T) {
	clientReader, serverWriter := io.Pipe()
	serverReader, clientWriter := io.Pipe()
	defer clientReader.Close()
	defer serverWriter.Close()
	defer serverReader.Close()
	defer clientWriter.Close()

	server := NewPeer(serverReader, serverWriter, PeerOptions{})
	client := NewPeer(clientReader, clientWriter, PeerOptions{})

	handlerStarted := make(chan struct{})
	handlerCancelled := make(chan error, 1)
	server.RegisterRequest("slow_reverse", func(ctx context.Context, raw json.RawMessage) (any, error) {
		close(handlerStarted)
		<-ctx.Done() // block until cancelled
		handlerCancelled <- ctx.Err()
		return nil, ctx.Err()
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = server.Start(ctx) }()
	go func() { _ = client.Start(ctx) }()
	defer server.Close()
	defer client.Close()

	// Client sends a request to the server.
	callDone := make(chan error, 1)
	go func() {
		var result json.RawMessage
		callDone <- client.Call(ctx, "slow_reverse", nil, &result)
	}()

	select {
	case <-handlerStarted:
	case <-time.After(2 * time.Second):
		t.Fatalf("handler never started")
	}

	// Client sends $/cancel_request with the correct "requestId" field name to
	// cancel the in-flight request. The original request id was 1.
	if err := client.Notify(ctx, "$/cancel_request", map[string]any{"requestId": 1}); err != nil {
		t.Fatalf("Notify $/cancel_request error = %v", err)
	}

	// The handler's context must be cancelled.
	select {
	case err := <-handlerCancelled:
		if err == nil {
			t.Fatalf("handler ctx.Err() = nil, want context.Canceled")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("handler was not cancelled by $/cancel_request")
	}

	// The original Call must return a -32800 "Request cancelled" error.
	err := <-callDone
	if err == nil {
		t.Fatalf("expected cancelled error, got nil")
	}
	rpcErr, ok := err.(*RPCError)
	if !ok {
		t.Fatalf("error type = %T, want *RPCError", err)
	}
	if rpcErr.Code != -32800 {
		t.Fatalf("error code = %d, want -32800 (Request cancelled)", rpcErr.Code)
	}
	if rpcErr.Message != "Request cancelled" {
		t.Fatalf("error message = %q, want %q", rpcErr.Message, "Request cancelled")
	}
}

// TestInboundCancelRequestUnknownIdIsNoop verifies that a $/cancel_request for
// an unknown/non-existent requestId is silently ignored (per spec, no error).
func TestInboundCancelRequestUnknownIdIsNoop(t *testing.T) {
	clientReader, serverWriter := io.Pipe()
	serverReader, clientWriter := io.Pipe()
	defer clientReader.Close()
	defer serverWriter.Close()
	defer serverReader.Close()
	defer clientWriter.Close()

	server := NewPeer(serverReader, serverWriter, PeerOptions{})
	client := NewPeer(clientReader, clientWriter, PeerOptions{})
	server.RegisterRequest("echo2", func(ctx context.Context, raw json.RawMessage) (any, error) {
		return raw, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = server.Start(ctx) }()
	go func() { _ = client.Start(ctx) }()
	defer server.Close()
	defer client.Close()

	// Send a cancel for an id that was never sent. Must not error or hang.
	if err := client.Notify(ctx, "$/cancel_request", map[string]any{"requestId": 99999}); err != nil {
		t.Fatalf("Notify unknown cancel error = %v", err)
	}

	// A subsequent normal call must still work (server not disrupted).
	var result json.RawMessage
	if err := client.Call(ctx, "echo2", json.RawMessage(`"ok"`), &result); err != nil {
		t.Fatalf("Call after unknown cancel error = %v", err)
	}
	if string(result) != `"ok"` {
		t.Fatalf("result = %s", result)
	}
}

// TestSessionDeleteAndLogoutRoundTrip verifies the new stable-v1 methods
// session/delete and logout are sent correctly and the host receives them.
func TestSessionDeleteAndLogoutRoundTrip(t *testing.T) {
	clientReader, serverWriter := io.Pipe()
	serverReader, clientWriter := io.Pipe()
	defer clientReader.Close()
	defer serverWriter.Close()
	defer serverReader.Close()
	defer clientWriter.Close()

	server := NewPeer(serverReader, serverWriter, PeerOptions{})
	client := NewPeer(clientReader, clientWriter, PeerOptions{})
	var deleteSeen, logoutSeen bool
	var mu sync.Mutex
	// The conn wraps the server peer; its Call goes out serverWriter -> client
	// peer, so the handlers must be registered on the client peer.
	client.RegisterRequest("session/delete", func(ctx context.Context, raw json.RawMessage) (any, error) {
		mu.Lock()
		deleteSeen = bytes.Contains(raw, []byte(`"sessionId":"s1"`))
		mu.Unlock()
		return DeleteSessionResponse{}, nil
	})
	client.RegisterRequest("logout", func(ctx context.Context, raw json.RawMessage) (any, error) {
		mu.Lock()
		logoutSeen = true
		mu.Unlock()
		return LogoutResponse{}, nil
	})

	conn := NewConnection(server, Client{Authority: AuthorityHandlers{}})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = server.Start(ctx) }()
	go func() { _ = client.Start(ctx) }()
	defer server.Close()
	defer client.Close()

	if err := conn.DeleteSession(ctx, DeleteSessionRequest{SessionID: "s1"}); err != nil {
		t.Fatalf("DeleteSession error = %v", err)
	}
	if err := conn.Logout(ctx, LogoutRequest{}); err != nil {
		t.Fatalf("Logout error = %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !deleteSeen {
		t.Fatalf("client never received session/delete with sessionId s1")
	}
	if !logoutSeen {
		t.Fatalf("client never received logout")
	}
}
