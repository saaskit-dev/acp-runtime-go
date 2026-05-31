package acpruntime

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func BenchmarkPeerCallRoundTrip(b *testing.B) {
	client, server, ctx, cancel := newBenchmarkPeerPair(b)
	defer cancel()
	defer client.Close()
	defer server.Close()

	params := json.RawMessage(`{"text":"hello","n":42}`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var result json.RawMessage
		if err := client.Call(ctx, "echo", params, &result); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPeerCallRawRoundTrip(b *testing.B) {
	client, server, ctx, cancel := newBenchmarkPeerPair(b)
	defer cancel()
	defer client.Close()
	defer server.Close()

	params := json.RawMessage(`{"text":"hello","n":42}`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := client.CallRaw(ctx, "echo", params); err != nil {
			b.Fatal(err)
		}
	}
}

func newBenchmarkPeerPair(b *testing.B) (*Peer, *Peer, context.Context, context.CancelFunc) {
	b.Helper()
	clientReader, serverWriter := io.Pipe()
	serverReader, clientWriter := io.Pipe()
	b.Cleanup(func() {
		_ = clientReader.Close()
		_ = serverWriter.Close()
		_ = serverReader.Close()
		_ = clientWriter.Close()
	})

	client := NewPeer(clientReader, clientWriter, PeerOptions{})
	server := NewPeer(serverReader, serverWriter, PeerOptions{})
	server.RegisterRequest("echo", func(ctx context.Context, raw json.RawMessage) (any, error) {
		return raw, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = client.Start(ctx) }()
	go func() { _ = server.Start(ctx) }()
	return client, server, ctx, cancel
}

func BenchmarkPeerNotify(b *testing.B) {
	peer := NewPeer(strings.NewReader(""), io.Discard, PeerOptions{})
	params := json.RawMessage(`{"sessionId":"s","update":{"sessionUpdate":"agent_message_chunk","type":"text","text":"hello"}}`)
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := peer.Notify(ctx, "session/update", params); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPeerNotifyRaw(b *testing.B) {
	peer := NewPeer(strings.NewReader(""), io.Discard, PeerOptions{})
	params := json.RawMessage(`{"sessionId":"s","update":{"sessionUpdate":"agent_message_chunk","type":"text","text":"hello"}}`)
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := peer.NotifyRaw(ctx, "session/update", params); err != nil {
			b.Fatal(err)
		}
	}
}
