package acpruntime

import "testing"

func TestWithSystemPromptHelpers(t *testing.T) {
	replace := WithSystemPrompt("Be concise.")
	if len(replace) != 1 || replace[SystemPromptMetaKey] != "Be concise." {
		t.Fatalf("WithSystemPrompt = %#v", replace)
	}
	appendMeta := WithAppendSystemPrompt("Prefer small diffs.")
	if len(appendMeta) != 1 || appendMeta[AppendSystemPromptMetaKey] != "Prefer small diffs." {
		t.Fatalf("WithAppendSystemPrompt = %#v", appendMeta)
	}
}

func TestMergeMeta(t *testing.T) {
	out := MergeMeta(
		WithSystemPrompt("host"),
		map[string]any{"custom": "v", "nested": map[string]any{"a": 1}},
		map[string]any{"custom": "override"},
		nil,
	)
	if out[SystemPromptMetaKey] != "host" {
		t.Fatalf("system prompt lost: %#v", out)
	}
	if out["custom"] != "override" {
		t.Fatalf("later key should win: %#v", out)
	}
	nested, ok := out["nested"].(map[string]any)
	if !ok || nested["a"] != 1 {
		t.Fatalf("nested meta lost: %#v", out["nested"])
	}
	if MergeMeta() != nil {
		t.Fatalf("empty MergeMeta should be nil")
	}
}

func TestPrepareAgentSessionStartAcceptsWithSystemPromptHelper(t *testing.T) {
	profile := ResolveAgentProfile(Agent{Type: CodexACPRegistryID})
	agent, meta, err := prepareAgentSessionStart(profile, StartSessionOptions{
		Agent: Agent{Type: CodexACPRegistryID, Command: "npm"},
		Meta:  MergeMeta(WithSystemPrompt("via helper"), map[string]any{"x": 1}),
	})
	if err != nil {
		t.Fatalf("prepareAgentSessionStart() error = %v", err)
	}
	if meta["x"] != 1 {
		t.Fatalf("meta = %#v", meta)
	}
	if _, ok := meta[SystemPromptMetaKey]; ok {
		t.Fatalf("reserved key should be consumed: %#v", meta)
	}
	cfg := decodeCodexConfig(t, agent.Env["CODEX_CONFIG"])
	if cfg["instructions"] != "via helper" {
		t.Fatalf("cfg = %#v", cfg)
	}
}

func TestPrepareAgentSessionStartCodexAppendUsesDeveloperInstructions(t *testing.T) {
	profile := ResolveAgentProfile(Agent{Type: CodexACPRegistryID})
	agent, _, err := prepareAgentSessionStart(profile, StartSessionOptions{
		Agent: Agent{
			Type:    CodexACPRegistryID,
			Command: "npm",
			Env: map[string]string{
				"CODEX_CONFIG": `{"developer_instructions":"existing"}`,
			},
		},
		Meta: WithAppendSystemPrompt("more"),
	})
	if err != nil {
		t.Fatalf("prepareAgentSessionStart() error = %v", err)
	}
	cfg := decodeCodexConfig(t, agent.Env["CODEX_CONFIG"])
	if cfg["developer_instructions"] != "existing\n\nmore" {
		t.Fatalf("cfg = %#v", cfg)
	}
	if _, ok := cfg["instructions"]; ok {
		t.Fatalf("append should not set instructions: %#v", cfg)
	}
}
