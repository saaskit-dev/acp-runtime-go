package acpruntime

import "testing"

func TestCreateClaudeCodeAgentUsesACPWrapper(t *testing.T) {
	agent := CreateClaudeCodeAgent(Agent{})
	if agent.Type != ClaudeCodeACPRegistryID {
		t.Fatalf("Type = %q, want %q", agent.Type, ClaudeCodeACPRegistryID)
	}
	if agent.Command != "npm" {
		t.Fatalf("Command = %q, want npm", agent.Command)
	}
	wantArgs := []string{"exec", "--yes", "@zed-industries/claude-agent-acp@0.23.1", "--"}
	if len(agent.Args) != len(wantArgs) {
		t.Fatalf("Args = %#v, want %#v", agent.Args, wantArgs)
	}
	for i, want := range wantArgs {
		if agent.Args[i] != want {
			t.Fatalf("Args[%d] = %q, want %q", i, agent.Args[i], want)
		}
	}
}
