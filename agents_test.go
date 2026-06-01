package acpruntime

import "testing"

func TestCreateCodexAgentUsesACPWrapper(t *testing.T) {
	agent := CreateCodexAgent(Agent{})
	if agent.Type != CodexACPRegistryID {
		t.Fatalf("Type = %q, want %q", agent.Type, CodexACPRegistryID)
	}
	if agent.Command != "npm" {
		t.Fatalf("Command = %q, want npm", agent.Command)
	}
	assertAgentArgs(t, agent.Args, []string{"exec", "--yes", "@zed-industries/codex-acp@0.15.0", "--"})
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
