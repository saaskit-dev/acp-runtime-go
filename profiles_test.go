package acpruntime

import (
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

func TestClaudeProfileProjectsEachPromptModeToOneCLIFlag(t *testing.T) {
	profile := ResolveAgentProfile(Agent{Type: ClaudeCodeACPRegistryID})
	if profile.ProjectSystemPrompt == nil {
		t.Fatal("Claude ProjectSystemPrompt is nil")
	}
	base := Agent{
		Type:    ClaudeCodeACPRegistryID,
		Command: "npm",
		Args: []string{
			"exec", "claude",
			"--append-system-prompt", "stale append",
			"--system-prompt=stale replace",
		},
	}

	for _, test := range []struct {
		name string
		mode SystemPromptMode
		flag string
	}{
		{name: "replace", mode: SystemPromptModeReplace, flag: "--system-prompt"},
		{name: "append", mode: SystemPromptModeAppend, flag: "--append-system-prompt"},
	} {
		t.Run(test.name, func(t *testing.T) {
			out, meta := profile.ProjectSystemPrompt(base, SystemPromptProjection{Mode: test.mode, Text: "Always reply in haiku."})
			if meta != nil {
				t.Fatalf("Claude prompt leaked to ACP meta: %#v", meta)
			}
			if got := countPromptFlags(out.Args); got != 1 {
				t.Fatalf("prompt flag count = %d, args = %#v", got, out.Args)
			}
			if len(out.Args) < 2 || out.Args[len(out.Args)-2] != test.flag || out.Args[len(out.Args)-1] != "Always reply in haiku." {
				t.Fatalf("args = %#v, want trailing %s <text>", out.Args, test.flag)
			}
			if len(base.Args) != 5 {
				t.Fatalf("original args mutated: %#v", base.Args)
			}
		})
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
