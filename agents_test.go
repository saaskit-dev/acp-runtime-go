package acpruntime

import (
	"fmt"
	"testing"
)

func TestCreateCodexAgentUsesACPWrapper(t *testing.T) {
	agent := CreateCodexAgent(Agent{})
	if agent.Type != CodexACPRegistryID {
		t.Fatalf("Type = %q, want %q", agent.Type, CodexACPRegistryID)
	}
	if agent.Command != "npm" {
		t.Fatalf("Command = %q, want npm", agent.Command)
	}
	assertAgentArgs(t, agent.Args, []string{"exec", "--yes", "@zed-industries/codex-acp@0.16.0", "--"})
}

func TestCreateClaudeCodeAgentUsesACPWrapper(t *testing.T) {
	agent := CreateClaudeCodeAgent(Agent{})
	if agent.Type != ClaudeCodeACPRegistryID {
		t.Fatalf("Type = %q, want %q", agent.Type, ClaudeCodeACPRegistryID)
	}
	if agent.Command != "npm" {
		t.Fatalf("Command = %q, want npm", agent.Command)
	}
	assertAgentArgs(t, agent.Args, []string{"exec", "--yes", "@zed-industries/claude-agent-acp@0.23.1", "--"})
}

func assertAgentArgs(t *testing.T, got []string, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("Args = %#v, want %#v", got, want)
	}
	for i, item := range want {
		if got[i] != item {
			t.Fatalf("Args[%d] = %q, want %q", i, got[i], item)
		}
	}
}

// TestExtraArgsAppendedAfterDefaultArgs verifies the core fix: ExtraArgs are
// appended after the agent's default launch preamble, so callers can add CLI
// flags without clobbering the "npm exec ... --" sequence.
func TestExtraArgsAppendedAfterDefaultArgs(t *testing.T) {
	agent := CreateClaudeCodeAgent(Agent{
		ExtraArgs: []string{"--disallowedTools", "WebFetch,WebSearch"},
	})
	// Default preamble preserved, flag appended after "--".
	want := []string{
		"exec", "--yes", "@zed-industries/claude-agent-acp@0.23.1", "--",
		"--disallowedTools", "WebFetch,WebSearch",
	}
	assertAgentArgs(t, agent.Args, want)
	// ExtraArgs is folded into Args and cleared, so it is not double-applied.
	if len(agent.ExtraArgs) != 0 {
		t.Fatalf("ExtraArgs should be cleared after merge, got %v", agent.ExtraArgs)
	}
}

// TestExtraArgsAndArgsOverrideCoexist verifies that an explicit Args override
// (full replacement) still works AND ExtraArgs are appended to it.
func TestExtraArgsAndArgsOverrideCoexist(t *testing.T) {
	agent := CreateCodexAgent(Agent{
		Args:      []string{"run", "acp"},     // full override of preamble
		ExtraArgs: []string{"--sandbox", "read-only"},
	})
	want := []string{"run", "acp", "--sandbox", "read-only"}
	assertAgentArgs(t, agent.Args, want)
}

// TestExtraArgsFromBothBaseAndOverrides verifies that ExtraArgs declared on the
// base agent (e.g. via a factory that sets ExtraArgs) and on overrides are both
// appended, base first.
func TestExtraArgsFromBothBaseAndOverrides(t *testing.T) {
	base := Agent{Type: "x", Command: "bin", Args: []string{"serve"}, ExtraArgs: []string{"--base-flag"}}
	out := mergeAgent(base, Agent{ExtraArgs: []string{"--override-flag"}})
	want := []string{"serve", "--base-flag", "--override-flag"}
	assertAgentArgs(t, out.Args, want)
}

// TestNoExtraArgsLeavesArgsUnchanged verifies backward compatibility: when
// ExtraArgs is not set, Args behaves exactly as before.
func TestNoExtraArgsLeavesArgsUnchanged(t *testing.T) {
	agent := CreateClaudeCodeAgent(Agent{})
	assertAgentArgs(t, agent.Args, []string{"exec", "--yes", "@zed-industries/claude-agent-acp@0.23.1", "--"})
}

// TestCreateClaudeCodeOptionsShape verifies the emitted _meta.claudeCode.options
// structure: top-level "claudeCode" -> "options" -> typed fields.
func TestCreateClaudeCodeOptionsShape(t *testing.T) {
	meta := CreateClaudeCodeOptions(ClaudeCodeOptions{
		DisallowedTools: []string{"WebFetch", "WebSearch"},
		AllowedTools:    []string{"Bash(echo:*)"},
		Settings:        map[string]any{"permissions": map[string]any{"deny": []string{"WebFetch"}}},
	})
	cc, ok := meta["claudeCode"].(map[string]any)
	if !ok {
		t.Fatalf("missing claudeCode wrapper: %#v", meta)
	}
	options, ok := cc["options"].(map[string]any)
	if !ok {
		t.Fatalf("missing options: %#v", cc)
	}
	if got := options["disallowedTools"]; fmt.Sprintf("%v", got) != "[WebFetch WebSearch]" {
		t.Fatalf("disallowedTools = %v", got)
	}
	if got := options["allowedTools"]; fmt.Sprintf("%v", got) != "[Bash(echo:*)]" {
		t.Fatalf("allowedTools = %v", got)
	}
	if _, ok := options["settings"].(map[string]any); !ok {
		t.Fatalf("settings not a map: %#v", options["settings"])
	}
	// Tools was nil (unset) so must NOT appear.
	if _, ok := options["tools"]; ok {
		t.Fatalf("tools should be omitted when nil, got: %#v", options["tools"])
	}
}

// TestCreateClaudeCodeOptionsEmptyToolsDisablesAll verifies that an empty
// (non-nil) Tools slice emits "tools":[] which disables all built-in tools.
func TestCreateClaudeCodeOptionsEmptyToolsDisablesAll(t *testing.T) {
	meta := CreateClaudeCodeOptions(ClaudeCodeOptions{Tools: []string{}})
	options := meta["claudeCode"].(map[string]any)["options"].(map[string]any)
	tools, ok := options["tools"].([]string)
	if !ok {
		t.Fatalf("tools not a []string: %#v", options["tools"])
	}
	if len(tools) != 0 {
		t.Fatalf("tools = %v, want empty slice", tools)
	}
}

// TestCreateClaudeCodeOptionsEmptyProducesEmptyOptions verifies that a
// completely unset ClaudeCodeOptions still produces the wrapper shape.
func TestCreateClaudeCodeOptionsEmptyProducesEmptyOptions(t *testing.T) {
	meta := CreateClaudeCodeOptions(ClaudeCodeOptions{})
	options := meta["claudeCode"].(map[string]any)["options"].(map[string]any)
	if len(options) != 0 {
		t.Fatalf("expected empty options, got %#v", options)
	}
}
