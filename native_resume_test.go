package acpruntime

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestCodexNativeResume proves session/load maps to codex thread/resume and
// keeps the ACP session id stable across the re-attach.
func TestCodexNativeResume(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	agent := CreateCodexNativeAgent(Agent{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestFakeCodexAppServer", "--"},
		Env:     map[string]string{"GO_WANT_HELPER_PROCESS": "1"},
	})
	runtime := NewRuntime(nil, RuntimeOptions{})
	session, err := runtime.LoadSession(ctx, LoadSessionOptions{
		StartSessionOptions: StartSessionOptions{Agent: agent, CWD: t.TempDir()},
		SessionID:           "fake-thread-1",
	})
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	defer func() { _ = session.Close(context.Background()) }()

	if sid := session.Snapshot().Session.ID; sid != "fake-thread-1" {
		t.Fatalf("session id = %q, want fake-thread-1 preserved across load", sid)
	}
	completion, err := session.Run(ctx, "continue")
	if err != nil {
		t.Fatalf("Run after load: %v", err)
	}
	if !strings.Contains(completion.OutputText, "FAKE_HELLO") {
		t.Fatalf("OutputText = %q", completion.OutputText)
	}
	// Engine probe results must surface through Diagnostics.
	diag := session.Diagnostics()
	if info, ok := diag.Raw["nativeEngine"].(map[string]any); !ok || info["engine"] != "codex" {
		t.Fatalf("diagnostics missing nativeEngine info: %+v", diag)
	}
}

// TestCodexNativeResumeFailure: a stale session id surfaces verbatim instead
// of silently starting a fresh conversation.
func TestCodexNativeResumeFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	agent := CreateCodexNativeAgent(Agent{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestFakeCodexAppServer", "--"},
		Env:     map[string]string{"GO_WANT_HELPER_PROCESS": "1"},
	})
	runtime := NewRuntime(nil, RuntimeOptions{})
	_, err := runtime.LoadSession(ctx, LoadSessionOptions{
		StartSessionOptions: StartSessionOptions{Agent: agent, CWD: t.TempDir()},
		SessionID:           "gone-thread",
	})
	if err == nil {
		t.Fatal("LoadSession succeeded against a gone thread id")
	}
	if !strings.Contains(err.Error(), "thread not found") {
		t.Fatalf("err = %v, want the engine's thread-not-found detail", err)
	}
}

// TestClaudeNativeResume: session/load respawns with --resume and the ACP
// session id stays the host's requested handle.
func TestClaudeNativeResume(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	agent := CreateClaudeCodeNativeAgent(Agent{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestFakeClaudeStreamJSON", "--"},
		Env:     map[string]string{"GO_WANT_HELPER_PROCESS": "1"},
	})
	runtime := NewRuntime(nil, RuntimeOptions{})
	session, err := runtime.LoadSession(ctx, LoadSessionOptions{
		StartSessionOptions: StartSessionOptions{Agent: agent, CWD: t.TempDir()},
		SessionID:           "claude-resume-target",
	})
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	defer func() { _ = session.Close(context.Background()) }()

	if sid := session.Snapshot().Session.ID; sid != "claude-resume-target" {
		t.Fatalf("session id = %q, want claude-resume-target", sid)
	}
	completion, err := session.Run(ctx, "continue")
	if err != nil {
		t.Fatalf("Run after load: %v", err)
	}
	if !strings.Contains(completion.OutputText, "FAKE_CLAUDE_HELLO") {
		t.Fatalf("OutputText = %q", completion.OutputText)
	}
}
