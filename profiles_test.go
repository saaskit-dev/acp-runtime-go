package acpruntime

import (
	"testing"
)

// TestDefaultProfileSystemPromptMeta asserts that the default profile emits
// _meta.systemPrompt for a non-empty prompt and emits nothing for an empty one
// (so an empty system prompt never clobbers the agent's own system prompt).
func TestDefaultProfileSystemPromptMeta(t *testing.T) {
	profile := defaultAgentProfile()
	if profile.CreateSystemPromptSessionMeta == nil {
		t.Fatalf("default CreateSystemPromptSessionMeta is nil")
	}

	meta := profile.CreateSystemPromptSessionMeta(SystemPrompt{Text: "Be concise."})
	if meta == nil {
		t.Fatalf("meta = nil, want systemPrompt entry")
	}
	got, ok := meta["systemPrompt"].(string)
	if !ok || got != "Be concise." {
		t.Fatalf("meta = %#v, want systemPrompt=Be concise.", meta)
	}

	if empty := profile.CreateSystemPromptSessionMeta(SystemPrompt{Text: ""}); empty != nil {
		t.Fatalf("empty prompt meta = %#v, want nil", empty)
	}
	if spaces := profile.CreateSystemPromptSessionMeta(SystemPrompt{Text: "   \n\t "}); spaces != nil {
		t.Fatalf("whitespace-only prompt meta = %#v, want nil", spaces)
	}
}

// TestDefaultProfileApplySystemPromptNil asserts the default profile does not
// rewrite agent args (only the Claude adapter does that via --append-system-prompt).
func TestDefaultProfileApplySystemPromptNil(t *testing.T) {
	profile := defaultAgentProfile()
	if profile.ApplySystemPromptToAgent != nil {
		t.Fatalf("default ApplySystemPromptToAgent should be nil; only agent-specific profiles set it")
	}
}

// TestClaudeProfileAppliesSystemPromptFlag asserts the Claude Code adapter
// profile appends --append-system-prompt to the agent's CLI args.
func TestClaudeProfileAppliesSystemPromptFlag(t *testing.T) {
	profile := ResolveAgentProfile(Agent{Type: ClaudeCodeACPRegistryID})
	if profile.ApplySystemPromptToAgent == nil {
		t.Fatalf("Claude ApplySystemPromptToAgent is nil")
	}
	base := Agent{Type: ClaudeCodeACPRegistryID, Command: "npm", Args: []string{"exec", "claude"}}
	out := profile.ApplySystemPromptToAgent(base, SystemPrompt{Text: "Always reply in haiku."})
	if len(out.Args) != 4 {
		t.Fatalf("Args = %v, want 4 elements", out.Args)
	}
	if out.Args[2] != "--append-system-prompt" || out.Args[3] != "Always reply in haiku." {
		t.Fatalf("appended args = %v, want --append-system-prompt <text>", out.Args[2:])
	}
	// Original args must be preserved and not mutated in place.
	if len(base.Args) != 2 {
		t.Fatalf("original base.Args was mutated: %v", base.Args)
	}
}

// TestClaudeProfileSkipsFlagForEmptyPrompt asserts empty/whitespace prompts do
// not inject the flag.
func TestClaudeProfileSkipsFlagForEmptyPrompt(t *testing.T) {
	profile := ResolveAgentProfile(Agent{Type: ClaudeCodeACPRegistryID})
	base := Agent{Type: ClaudeCodeACPRegistryID, Command: "npm", Args: []string{"exec"}}
	for _, text := range []string{"", "   ", "\n\t"} {
		out := profile.ApplySystemPromptToAgent(base, SystemPrompt{Text: text})
		if len(out.Args) != 1 {
			t.Fatalf("prompt %q produced args %v, want unchanged", text, out.Args)
		}
	}
}

// TestClaudeProfileEmitsSystemPromptMeta asserts the Claude profile still
// inherits the default _meta.systemPrompt behavior (belt-and-suspenders: the
// CLI flag is the primary path, _meta covers agents reading it directly).
func TestClaudeProfileEmitsSystemPromptMeta(t *testing.T) {
	profile := ResolveAgentProfile(Agent{Type: ClaudeCodeACPRegistryID})
	if profile.CreateSystemPromptSessionMeta == nil {
		t.Fatalf("Claude profile lost the default CreateSystemPromptSessionMeta")
	}
	meta := profile.CreateSystemPromptSessionMeta(SystemPrompt{Text: "hi"})
	if meta["systemPrompt"] != "hi" {
		t.Fatalf("meta = %#v, want systemPrompt=hi", meta)
	}
}

// TestAllKnownProfilesHaveSystemPromptMeta asserts every agent type that the
// runtime knows about gets the default _meta.systemPrompt behavior (none
// should silently drop a host system prompt).
func TestAllKnownProfilesHaveSystemPromptMeta(t *testing.T) {
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
		"", // unknown agent falls through to default profile
	}
	for _, agentType := range agentTypes {
		profile := ResolveAgentProfile(Agent{Type: agentType})
		if profile.CreateSystemPromptSessionMeta == nil {
			t.Errorf("agent %q has nil CreateSystemPromptSessionMeta", agentType)
			continue
		}
		meta := profile.CreateSystemPromptSessionMeta(SystemPrompt{Text: "x"})
		if meta == nil || meta["systemPrompt"] != "x" {
			t.Errorf("agent %q meta = %#v, want systemPrompt=x", agentType, meta)
		}
	}
}

// TestResolveAgentProfileCoverage is a sanity guard: ensures the profile
// returned for every known agent still has the non-optional fields populated.
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
