package acpruntime

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestCodexNativeTransportLive drives the REAL codex CLI (codex app-server)
// through the native bridge. Opt-in: it needs a logged-in codex install, so
// it is skipped unless ACP_LIVE_CODEX=1. CODEX_NATIVE_TEST_HOME optionally
// redirects CODEX_HOME (sandboxed runs point it at a prepared copy).
func TestCodexNativeTransportLive(t *testing.T) {
	if os.Getenv("ACP_LIVE_CODEX") != "1" {
		t.Skip("set ACP_LIVE_CODEX=1 to run against the real codex CLI")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	agent := CreateCodexNativeAgent(Agent{})
	if home := os.Getenv("CODEX_NATIVE_TEST_HOME"); home != "" {
		agent.Env = map[string]string{"CODEX_HOME": home}
	}
	runtime := NewRuntime(nil, RuntimeOptions{})
	session, err := runtime.StartSession(ctx, StartSessionOptions{Agent: agent, CWD: "/tmp"})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer func() { _ = session.Close(context.Background()) }()

	completion, err := session.Run(ctx, "Reply with exactly and only: NATIVE_LIVE_OK")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	t.Logf("output=%q stopReason=%s usage=%+v", completion.OutputText, completion.StopReason, completion.Usage)
	if !strings.Contains(completion.OutputText, "NATIVE_LIVE_OK") {
		t.Fatalf("OutputText = %q, want NATIVE_LIVE_OK", completion.OutputText)
	}
	if completion.Usage == nil || completion.Usage.InputTokens == 0 {
		t.Fatalf("Usage missing: %+v", completion.Usage)
	}
}
