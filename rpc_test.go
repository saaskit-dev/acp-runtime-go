package acpruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"reflect"
	"strings"
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
