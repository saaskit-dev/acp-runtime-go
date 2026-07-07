package acpruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestApplyClaudeAgentConfig verifies the Claude profile translates AgentConfig
// into _meta.claudeCode.options with disallowedTools + settings.permissions.
func TestApplyClaudeAgentConfig(t *testing.T) {
	profile := ResolveAgentProfile(Agent{Type: ClaudeCodeACPRegistryID})
	if profile.ApplyAgentConfig == nil {
		t.Fatalf("Claude profile has no ApplyAgentConfig")
	}
	agent, meta := profile.ApplyAgentConfig(Agent{Type: ClaudeCodeACPRegistryID}, AgentConfig{
		DisallowedTools: []string{"WebFetch"},
		AllowedTools:    []string{"Bash(echo:*)"},
		Permissions: PermissionConfig{
			Deny: []string{"Read(./.env*)"},
		},
	})
	_ = agent // agent is unchanged for Claude
	if meta == nil {
		t.Fatalf("meta is nil")
	}
	cc, ok := meta["claudeCode"].(map[string]any)
	if !ok {
		t.Fatalf("missing claudeCode wrapper: %#v", meta)
	}
	options, ok := cc["options"].(map[string]any)
	if !ok {
		t.Fatalf("missing options: %#v", cc)
	}
	// disallowedTools
	dt, ok := options["disallowedTools"].([]string)
	if !ok {
		t.Fatalf("disallowedTools not []string: %#v", options["disallowedTools"])
	}
	if len(dt) != 1 || dt[0] != "WebFetch" {
		t.Fatalf("disallowedTools = %v", dt)
	}
	// allowedTools
	at, ok := options["allowedTools"].([]string)
	if !ok {
		t.Fatalf("allowedTools not []string: %#v", options["allowedTools"])
	}
	if len(at) != 1 || at[0] != "Bash(echo:*)" {
		t.Fatalf("allowedTools = %v", at)
	}
	settings, ok := options["settings"].(map[string]any)
	if !ok {
		t.Fatalf("missing settings")
	}
	perm, ok := settings["permissions"].(map[string]any)
	if !ok {
		t.Fatalf("missing permissions")
	}
	deny, ok := perm["deny"].([]string)
	if !ok {
		t.Fatalf("deny not []string: %#v", perm["deny"])
	}
	if len(deny) != 1 || deny[0] != "Read(./.env*)" {
		t.Fatalf("deny = %v", deny)
	}
}

// TestApplyClaudeAgentConfigEmpty verifies empty AgentConfig produces empty meta.
func TestApplyClaudeAgentConfigEmpty(t *testing.T) {
	profile := ResolveAgentProfile(Agent{Type: ClaudeCodeACPRegistryID})
	_, meta := profile.ApplyAgentConfig(Agent{}, AgentConfig{})
	// CreateClaudeCodeOptions(empty) still produces {"claudeCode":{"options":{}}}
	cc, ok := meta["claudeCode"].(map[string]any)
	if !ok {
		t.Fatalf("expected claudeCode wrapper even when empty")
	}
	options := cc["options"].(map[string]any)
	if len(options) != 0 {
		t.Fatalf("expected empty options, got %#v", options)
	}
}

// TestApplyCodexAgentConfig verifies the Codex profile translates AgentConfig
// into CODEX_CONFIG env JSON with sandbox_mode + model.
func TestApplyCodexAgentConfig(t *testing.T) {
	profile := ResolveAgentProfile(Agent{Type: CodexACPRegistryID})
	if profile.ApplyAgentConfig == nil {
		t.Fatalf("Codex profile has no ApplyAgentConfig")
	}
	agent, meta := profile.ApplyAgentConfig(Agent{Type: CodexACPRegistryID}, AgentConfig{
		Model:   "deepseek-chat",
		Sandbox: "read-only",
	})
	if meta != nil {
		t.Fatalf("codex should return nil meta, got %#v", meta)
	}
	if agent.Env == nil {
		t.Fatalf("agent.Env is nil")
	}
	codexConfig := agent.Env["CODEX_CONFIG"]
	if codexConfig == "" {
		t.Fatalf("CODEX_CONFIG env is empty")
	}
	var config map[string]any
	if err := json.Unmarshal([]byte(codexConfig), &config); err != nil {
		t.Fatalf("CODEX_CONFIG not valid JSON: %v", err)
	}
	if config["model"] != "deepseek-chat" {
		t.Fatalf("model = %v, want deepseek-chat", config["model"])
	}
	if config["sandbox_mode"] != "read-only" {
		t.Fatalf("sandbox_mode = %v, want read-only", config["sandbox_mode"])
	}
}

// TestApplyCodexAgentConfigFullAccess verifies sandbox mapping.
func TestApplyCodexAgentConfigFullAccess(t *testing.T) {
	profile := ResolveAgentProfile(Agent{Type: CodexACPRegistryID})
	agent, _ := profile.ApplyAgentConfig(Agent{}, AgentConfig{Sandbox: "full-access"})
	var config map[string]any
	json.Unmarshal([]byte(agent.Env["CODEX_CONFIG"]), &config)
	if config["sandbox_mode"] != "danger-full-access" {
		t.Fatalf("sandbox_mode = %v, want danger-full-access", config["sandbox_mode"])
	}
}

// TestApplyOpenCodeAgentConfig verifies model goes to _meta.
func TestApplyOpenCodeAgentConfig(t *testing.T) {
	profile := ResolveAgentProfile(Agent{Type: OpenCodeACPRegistryID})
	if profile.ApplyAgentConfig == nil {
		t.Fatalf("OpenCode profile has no ApplyAgentConfig")
	}
	agent, meta := profile.ApplyAgentConfig(Agent{}, AgentConfig{
		Model: "glm-5.2",
		Extra: map[string]any{"provider": "zai"},
	})
	_ = agent
	if meta["model"] != "glm-5.2" {
		t.Fatalf("model = %v", meta["model"])
	}
	if meta["provider"] != "zai" {
		t.Fatalf("provider = %v", meta["provider"])
	}
}

// TestApplyAgentConfigUnknownAgent verifies agents without ApplyAgentConfig
// return nil meta (best-effort skipped).
func TestApplyAgentConfigUnknownAgent(t *testing.T) {
	profile := ResolveAgentProfile(Agent{Type: "unknown-agent"})
	if profile.ApplyAgentConfig != nil {
		t.Fatalf("unknown agent should have nil ApplyAgentConfig")
	}
}

// TestCreateCodexConfig verifies the convenience function produces valid JSON.
func TestCreateCodexConfig(t *testing.T) {
	env, err := CreateCodexConfig(CodexConfig{
		Model:          "gpt-5.5",
		SandboxMode:    "workspace-write",
		ApprovalPolicy: "on-request",
	})
	if err != nil {
		t.Fatalf("CreateCodexConfig error: %v", err)
	}
	var config map[string]any
	if err := json.Unmarshal([]byte(env["CODEX_CONFIG"]), &config); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if config["model"] != "gpt-5.5" {
		t.Fatalf("model = %v", config["model"])
	}
	if config["sandbox_mode"] != "workspace-write" {
		t.Fatalf("sandbox_mode = %v", config["sandbox_mode"])
	}
	if config["approval_policy"] != "on-request" {
		t.Fatalf("approval_policy = %v", config["approval_policy"])
	}
}

// TestCreateCodexConfigMerge verifies existing CODEX_CONFIG is preserved.
func TestCreateCodexConfigMerge(t *testing.T) {
	env, _ := CreateCodexConfig(CodexConfig{Model: "gpt-5.5"})
	// Second call should merge, not clobber
	env2, _ := buildCodexEnv(CodexConfig{SandboxMode: "read-only"}, env)
	var config map[string]any
	json.Unmarshal([]byte(env2["CODEX_CONFIG"]), &config)
	if config["model"] != "gpt-5.5" {
		t.Fatalf("model lost in merge: %v", config["model"])
	}
	if config["sandbox_mode"] != "read-only" {
		t.Fatalf("sandbox lost in merge: %v", config["sandbox_mode"])
	}
}

// TestWriteOpenCodeConfig verifies file creation and content.
func TestWriteOpenCodeConfig(t *testing.T) {
	cwd := t.TempDir()
	err := WriteOpenCodeConfig(cwd, OpenCodeConfig{
		Model:    "glm-5.2",
		Provider: "zai",
		Permission: OpenCodePermission{
			Deny:  []string{"bash"},
			Allow: []string{"read"},
		},
	})
	if err != nil {
		t.Fatalf("WriteOpenCodeConfig error: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(cwd, "opencode.json"))
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}
	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if config["model"] != "glm-5.2" {
		t.Fatalf("model = %v", config["model"])
	}
	if config["provider"] != "zai" {
		t.Fatalf("provider = %v", config["provider"])
	}
	perm, ok := config["permission"].(map[string]any)
	if !ok {
		t.Fatalf("missing permission")
	}
	if fmtSlice(perm["deny"]) != "[bash]" {
		t.Fatalf("deny = %v", perm["deny"])
	}
}

// TestAgentConfigViaStartSessionClaude verifies AgentConfig reaches the wire
// when used via StartSessionOptions for a Claude agent (simulator-based).
func TestAgentConfigViaStartSessionClaude(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cwd := t.TempDir()
	storage := t.TempDir()
	simulatorBin := buildSimulatorBinary(t)
	agent := Agent{
		Type:    LocalSimulatorAgentACPRegistryID,
		Command: simulatorBin,
		Args:    []string{"--auth-mode", "none", "--storage-dir", storage},
	}
	var capturedNew []byte
	var mu sync.Mutex
	factory := NewStdioConnectionFactory(StdioFactoryOptions{
		OnACPMessage: func(dir string, msg []byte) {
			if dir == "outbound" && bytes.Contains(msg, []byte(`"session/new"`)) {
				mu.Lock()
				capturedNew = append(capturedNew[:0], msg...)
				mu.Unlock()
			}
		},
	})
	runtime := NewRuntime(factory, RuntimeOptions{})
	// Use ClaudeCodeACPRegistryID type so the Claude profile's ApplyAgentConfig
	// fires (even though we spawn a simulator for determinism).
	agent.Type = ClaudeCodeACPRegistryID
	session, err := runtime.StartSession(ctx, StartSessionOptions{
		Agent: agent,
		CWD:   cwd,
		AgentConfig: &AgentConfig{
			DisallowedTools: []string{"WebFetch"},
		},
	})
	if err != nil {
		t.Fatalf("StartSession error = %v", err)
	}
	defer session.Close(context.Background())
	mu.Lock()
	snap := append([]byte(nil), capturedNew...)
	mu.Unlock()
	if !bytes.Contains(snap, []byte(`"disallowedTools"`)) {
		t.Fatalf("AgentConfig.DisallowedTools did not reach session/new wire: %s", snap)
	}
}

func fmtSlice(v any) string {
	if s, ok := v.([]string); ok {
		b, _ := json.Marshal(s)
		return string(b)
	}
	b, _ := json.Marshal(v)
	// json.Marshal produces ["a","b"] but we compare with [a b] format in some tests
	// normalize to [a b]
	var arr []string
	if json.Unmarshal(b, &arr) == nil {
		result := "["
		for i, s := range arr {
			if i > 0 {
				result += " "
			}
			result += s
		}
		return result + "]"
	}
	return string(b)
}
