package acpruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
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
