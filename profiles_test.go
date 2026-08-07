package acpruntime

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDefaultProfileProjectsReplaceAndAppendToMeta(t *testing.T) {
	profile := defaultAgentProfile()
	if profile.ProjectSystemPrompt == nil {
		t.Fatal("default ProjectSystemPrompt is nil")
	}

	base := Agent{Type: LocalSimulatorAgentACPRegistryID}
	for _, test := range []struct {
		name string
		mode SystemPromptMode
		key  string
	}{
		{name: "replace", mode: SystemPromptModeReplace, key: SystemPromptMetaKey},
		{name: "append", mode: SystemPromptModeAppend, key: AppendSystemPromptMetaKey},
	} {
		t.Run(test.name, func(t *testing.T) {
			out, meta := profile.ProjectSystemPrompt(base, SystemPromptProjection{Mode: test.mode, Text: "Be concise."})
			if out.Type != base.Type {
				t.Fatalf("agent changed: %#v", out)
			}
			if len(meta) != 1 || meta[test.key] != "Be concise." {
				t.Fatalf("meta = %#v, want only %s", meta, test.key)
			}
		})
	}
}

func TestClaudeProfileProjectsPromptModesToSessionMeta(t *testing.T) {
	profile := ResolveAgentProfile(Agent{Type: ClaudeCodeACPRegistryID})
	if profile.ProjectSystemPrompt == nil {
		t.Fatal("Claude ProjectSystemPrompt is nil")
	}
	base := Agent{
		Type:    ClaudeCodeACPRegistryID,
		Command: "npm",
		Args: []string{
			"exec", "--yes", "@agentclientprotocol/claude-agent-acp", "--",
			"--append-system-prompt", "stale append",
			"--system-prompt=stale replace",
		},
	}

	t.Run("replace", func(t *testing.T) {
		out, meta := profile.ProjectSystemPrompt(base, SystemPromptProjection{
			Mode: SystemPromptModeReplace,
			Text: "Always reply in haiku.",
		})
		if countPromptFlags(out.Args) != 0 {
			t.Fatalf("stale CLI prompt flags not stripped: %#v", out.Args)
		}
		if len(meta) != 1 || meta[SystemPromptMetaKey] != "Always reply in haiku." {
			t.Fatalf("meta = %#v, want systemPrompt string", meta)
		}
		if len(base.Args) != 7 {
			t.Fatalf("original args mutated: %#v", base.Args)
		}
	})

	t.Run("append", func(t *testing.T) {
		out, meta := profile.ProjectSystemPrompt(base, SystemPromptProjection{
			Mode: SystemPromptModeAppend,
			Text: "Always reply in haiku.",
		})
		if countPromptFlags(out.Args) != 0 {
			t.Fatalf("stale CLI prompt flags not stripped: %#v", out.Args)
		}
		raw, ok := meta[SystemPromptMetaKey].(map[string]any)
		if !ok {
			t.Fatalf("meta = %#v, want systemPrompt object", meta)
		}
		if raw["type"] != "preset" || raw["preset"] != "claude_code" || raw["append"] != "Always reply in haiku." {
			t.Fatalf("systemPrompt object = %#v", raw)
		}
	})
}

func TestCodexProfileProjectsPromptToCodexConfigEnv(t *testing.T) {
	profile := ResolveAgentProfile(Agent{Type: CodexACPRegistryID})
	if profile.ProjectSystemPrompt == nil {
		t.Fatal("Codex ProjectSystemPrompt is nil")
	}

	t.Run("replace writes instructions", func(t *testing.T) {
		base := Agent{
			Type: CodexACPRegistryID,
			Env: map[string]string{
				"CODEX_CONFIG": `{"model":"gpt-5.5","developer_instructions":"keep-dev","instructions":"stale"}`,
				"OTHER":        "keep",
			},
		}
		out, meta := profile.ProjectSystemPrompt(base, SystemPromptProjection{
			Mode: SystemPromptModeReplace,
			Text: "Be terse.",
		})
		if meta != nil {
			t.Fatalf("Codex should not project prompt meta, got %#v", meta)
		}
		if out.Env["OTHER"] != "keep" {
			t.Fatalf("non-CODEX env lost: %#v", out.Env)
		}
		cfg := decodeCodexConfig(t, out.Env["CODEX_CONFIG"])
		if cfg["model"] != "gpt-5.5" {
			t.Fatalf("existing CODEX_CONFIG fields lost: %#v", cfg)
		}
		if cfg["instructions"] != "Be terse." {
			t.Fatalf("instructions = %#v, want replace text", cfg["instructions"])
		}
		// Replace must not clobber an independent developer layer.
		if cfg["developer_instructions"] != "keep-dev" {
			t.Fatalf("developer_instructions should be preserved: %#v", cfg["developer_instructions"])
		}
		// Original env map must not be mutated.
		if base.Env["CODEX_CONFIG"] != `{"model":"gpt-5.5","developer_instructions":"keep-dev","instructions":"stale"}` {
			t.Fatalf("base env mutated: %#v", base.Env)
		}
	})

	t.Run("append concatenates developer_instructions", func(t *testing.T) {
		base := Agent{
			Type: CodexACPRegistryID,
			Env: map[string]string{
				"CODEX_CONFIG": `{"developer_instructions":"base block","instructions":"leave me"}`,
			},
		}
		out, meta := profile.ProjectSystemPrompt(base, SystemPromptProjection{
			Mode: SystemPromptModeAppend,
			Text: "extra block",
		})
		if meta != nil {
			t.Fatalf("meta = %#v, want nil", meta)
		}
		cfg := decodeCodexConfig(t, out.Env["CODEX_CONFIG"])
		if cfg["developer_instructions"] != "base block\n\nextra block" {
			t.Fatalf("developer_instructions = %#v", cfg["developer_instructions"])
		}
		if cfg["instructions"] != "leave me" {
			t.Fatalf("append must not touch instructions: %#v", cfg["instructions"])
		}
	})

	t.Run("empty env creates CODEX_CONFIG instructions", func(t *testing.T) {
		base := Agent{Type: CodexACPRegistryID}
		out, _ := profile.ProjectSystemPrompt(base, SystemPromptProjection{
			Mode: SystemPromptModeReplace,
			Text: "from empty",
		})
		cfg := decodeCodexConfig(t, out.Env["CODEX_CONFIG"])
		if cfg["instructions"] != "from empty" {
			t.Fatalf("cfg = %#v", cfg)
		}
		if _, ok := cfg["developer_instructions"]; ok {
			t.Fatalf("replace should not set developer_instructions: %#v", cfg)
		}
	})
}

func TestPrepareAgentSessionStartCodexSystemPromptToEnv(t *testing.T) {
	profile := ResolveAgentProfile(Agent{Type: CodexACPRegistryID})
	agent, meta, err := prepareAgentSessionStart(profile, StartSessionOptions{
		Agent: Agent{
			Type:    CodexACPRegistryID,
			Command: "npm",
			Args:    []string{"exec", "--yes", "@agentclientprotocol/codex-acp", "--"},
			Env:     map[string]string{"CODEX_CONFIG": `{"sandbox_mode":"read-only"}`},
		},
		Meta: map[string]any{
			SystemPromptMetaKey: "Host prompt.",
			"custom":            "kept",
		},
	})
	if err != nil {
		t.Fatalf("prepareAgentSessionStart() error = %v", err)
	}
	if meta["custom"] != "kept" {
		t.Fatalf("caller meta lost: %#v", meta)
	}
	if _, ok := meta[SystemPromptMetaKey]; ok {
		t.Fatalf("reserved systemPrompt should be consumed, meta=%#v", meta)
	}
	cfg := decodeCodexConfig(t, agent.Env["CODEX_CONFIG"])
	if cfg["sandbox_mode"] != "read-only" {
		t.Fatalf("sandbox_mode lost: %#v", cfg)
	}
	if cfg["instructions"] != "Host prompt." {
		t.Fatalf("instructions = %#v", cfg["instructions"])
	}
}

func TestAllKnownProfilesProjectSystemPrompt(t *testing.T) {
	agentTypes := []string{
		CodexACPRegistryID,
		ClaudeCodeACPRegistryID,
		GeminiCLIACPRegistryID,
		GitHubCopilotACPRegistryID,
		OpenCodeACPRegistryID,
		PiACPRegistryID,
		CursorACPRegistryID,
		SimulatorAgentACPRegistryID,
		LocalSimulatorAgentACPRegistryID,
		"",
	}
	for _, agentType := range agentTypes {
		profile := ResolveAgentProfile(Agent{Type: agentType})
		if profile.ProjectSystemPrompt == nil {
			t.Errorf("agent %q has nil ProjectSystemPrompt", agentType)
		}
	}
}

func TestResolveAgentProfileCoverage(t *testing.T) {
	for _, agentType := range []string{CodexACPRegistryID, ClaudeCodeACPRegistryID, ""} {
		profile := ResolveAgentProfile(Agent{Type: agentType})
		if profile.CreateInitialConfigAliases == nil || profile.MapOperationKind == nil {
			t.Errorf("agent %q profile missing required fields", agentType)
		}
		if profile.MapOperationKind("execute") != "execute_command" {
			t.Errorf("agent %q MapOperationKind(execute) = %q", agentType, profile.MapOperationKind("execute"))
		}
	}
}

func countPromptFlags(args []string) int {
	count := 0
	for _, arg := range args {
		if arg == "--system-prompt" || arg == "--append-system-prompt" ||
			strings.HasPrefix(arg, "--system-prompt=") ||
			strings.HasPrefix(arg, "--append-system-prompt=") {
			count++
		}
	}
	return count
}

func decodeCodexConfig(t *testing.T, raw string) map[string]any {
	t.Helper()
	if raw == "" {
		t.Fatal("CODEX_CONFIG empty")
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("CODEX_CONFIG json: %v (%s)", err, raw)
	}
	return cfg
}
