package acpruntime

import (
	"context"
	"regexp"
	"testing"
)

var nativePlatformSessionIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func TestNativeCodexSupportsPlatformConfiguration(t *testing.T) {
	profile := ResolveAgentProfile(CreateCodexNativeAgent(Agent{}))
	if profile.ApplyAgentConfig == nil {
		t.Fatal("codex-native has no AgentConfig projection: platform model/sandbox authority would be ignored")
	}
	agent, _ := profile.ApplyAgentConfig(CreateCodexNativeAgent(Agent{}), AgentConfig{Model: "test-model", Sandbox: "read-only"})
	if agent.Env["CODEX_CONFIG"] == "" {
		t.Fatal("codex-native did not project model/sandbox into CODEX_CONFIG")
	}
}

func TestNativeClaudeUsesResumableSessionIdentity(t *testing.T) {
	engine := &claudeNativeEngine{procs: map[string]*claudeProc{}}
	id, err := engine.NewSession(context.Background(), nativeEngineOptions{}, NewSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !nativePlatformSessionIDPattern.MatchString(id) {
		t.Fatalf("provider session id %q cannot be passed to Claude --resume / --session-id", id)
	}
}
