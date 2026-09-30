package acpruntime

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// TestClaudeNativeMultimodalContent: ACP image/resource blocks map onto
// anthropic message content shapes.
func TestClaudeNativeMultimodalContent(t *testing.T) {
	content := claudeContentFromBlocks([]ContentBlock{
		{Type: "text", Text: "look:"},
		{Type: "image", MimeType: "image/jpeg", Data: "aGVsbG8="},
		{Type: "image", URI: "https://example.com/x.png"},
		{Type: "resource_link", Name: "doc", URI: "file:///d.md"},
		{Type: "resource", Resource: json.RawMessage(`{"uri":"file:///r.md","text":"body"}`)},
		{Type: "audio", Data: "xx"},
	})
	if len(content) != 5 {
		t.Fatalf("content = %+v, want 5 items (audio skipped)", content)
	}
	img := content[1]
	if img["type"] != "image" {
		t.Fatalf("item 1 = %+v", img)
	}
	src := img["source"].(map[string]any)
	if src["type"] != "base64" || src["media_type"] != "image/jpeg" || src["data"] != "aGVsbG8=" {
		t.Fatalf("base64 source = %+v", src)
	}
	if content[2]["source"].(map[string]any)["type"] != "url" {
		t.Fatalf("url source = %+v", content[2])
	}
}

// TestCodexNativeMultimodalInput: ACP blocks map onto codex input items
// (text + input_image data URLs; resources degrade to text).
func TestCodexNativeMultimodalInput(t *testing.T) {
	items := codexInputFromBlocks([]ContentBlock{
		{Type: "text", Text: "look:"},
		{Type: "image", MimeType: "image/png", Data: "QUJD"},
		{Type: "image", URI: "https://example.com/x.png"},
		{Type: "resource", Resource: json.RawMessage(`{"text":"body"}`)},
		{Type: "audio", Data: "xx"},
	})
	if len(items) != 4 {
		t.Fatalf("items = %+v, want 4 (audio skipped)", items)
	}
	img := items[1].(map[string]any)
	if img["type"] != "input_image" || img["image_url"] != "data:image/png;base64,QUJD" {
		t.Fatalf("image item = %+v", img)
	}
	if items[2].(map[string]any)["image_url"] != "https://example.com/x.png" {
		t.Fatalf("url image = %+v", items[2])
	}
}

// TestCodexApprovalDeniedAndApproved: server-initiated approval requests map
// onto session/request_permission; authority decisions drive the response.
func TestCodexApprovalDeniedAndApproved(t *testing.T) {
	newTestProc := func(decision PermissionDecision, called *bool) *codexDaemon {
		e := &codexNativeEngine{}
		e.opts = nativeEngineOptions{
			Agent: Agent{Command: "codex"},
			requestPermission: func(ctx context.Context, req PermissionRequest) (PermissionDecision, error) {
				*called = true
				return decision, nil
			},
		}
		daemon := &codexDaemon{key: "test", eng: e, turns: map[string]*codexTurn{}}
		daemon.mu.Lock()
		daemon.turns["t1"] = &codexTurn{done: make(chan struct{})}
		daemon.mu.Unlock()
		return daemon
	}

	deniedCalled := false
	p := newTestProc(PermissionDecision{Outcome: "cancelled"}, &deniedCalled)
	resp, err := p.handleApproval(context.Background(), json.RawMessage(
		`{"threadId":"t1","callId":"c1","command":["rm","-rf","/"]}`))
	if err != nil {
		t.Fatalf("handleApproval: %v", err)
	}
	if !deniedCalled {
		t.Fatal("authority was not consulted")
	}
	if resp.(map[string]any)["decision"] != "denied" {
		t.Fatalf("denied response = %+v", resp)
	}

	approvedCalled := false
	p2 := newTestProc(PermissionDecision{Outcome: "selected", OptionID: "approve"}, &approvedCalled)
	resp2, err := p2.handleApproval(context.Background(), json.RawMessage(
		`{"threadId":"t1","callId":"c2","command":["ls"]}`))
	if err != nil {
		t.Fatalf("handleApproval: %v", err)
	}
	if resp2.(map[string]any)["decision"] != "approved" {
		t.Fatalf("approved response = %+v", resp2)
	}
}

// TestClaudeNativeFork: session/fork spawns with --resume <id> --fork-session
// and returns a fresh resumable UUID.
func TestClaudeNativeFork(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") == "1" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	agent := CreateClaudeCodeNativeAgent(Agent{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestFakeClaudeStreamJSON", "--"},
		Env:     map[string]string{"GO_WANT_HELPER_PROCESS": "1"},
	})
	runtime := NewRuntime(nil, RuntimeOptions{})
	session, err := runtime.ForkSession(ctx, ForkSessionOptions{
		StartSessionOptions: StartSessionOptions{Agent: agent, CWD: t.TempDir()},
		SessionID:           "orig-1",
	})
	if err != nil {
		t.Fatalf("ForkSession: %v", err)
	}
	defer func() { _ = session.Close(context.Background()) }()

	sid := session.Snapshot().Session.ID
	if !nativePlatformSessionIDPattern.MatchString(sid) {
		t.Fatalf("session id = %q, want resumable UUID for the forked lineage", sid)
	}
	completion, err := session.Run(ctx, "hi")
	if err != nil {
		t.Fatalf("Run after fork: %v", err)
	}
	if !strings.Contains(completion.OutputText, "FORKED") {
		t.Fatalf("OutputText = %q, want FORKED marker (spawn carried --fork-session)", completion.OutputText)
	}
}

// TestCodexNativeForkUnsupported: codex native has no fork translation; the
// error must say so instead of fabricating a session.
func TestCodexNativeForkUnsupported(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") == "1" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	agent := CreateCodexNativeAgent(Agent{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestFakeCodexAppServer", "--"},
		Env:     map[string]string{"GO_WANT_HELPER_PROCESS": "1"},
	})
	runtime := NewRuntime(nil, RuntimeOptions{})
	_, err := runtime.ForkSession(ctx, ForkSessionOptions{
		StartSessionOptions: StartSessionOptions{Agent: agent, CWD: t.TempDir()},
		SessionID:           "fake-thread-1",
	})
	if err == nil || !strings.Contains(err.Error(), "fork") {
		t.Fatalf("err = %v, want explicit fork-unsupported error", err)
	}
}
