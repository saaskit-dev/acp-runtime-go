package acpruntime

const (
	CodexACPRegistryID               = "codex-acp"
	ClaudeCodeACPRegistryID          = "claude-acp"
	GeminiCLIACPRegistryID           = "gemini"
	GitHubCopilotACPRegistryID       = "github-copilot-cli"
	OpenCodeACPRegistryID            = "opencode"
	PiACPRegistryID                  = "pi-acp"
	CursorACPRegistryID              = "cursor"
	SimulatorAgentACPRegistryID      = "simulator-agent-acp"
	LocalSimulatorAgentACPRegistryID = "local-simulator-agent-acp"
)

func CreateCodexAgent(overrides Agent) Agent {
	return mergeAgent(Agent{Type: CodexACPRegistryID, Command: "codex", Args: []string{"acp"}}, overrides)
}

func CreateClaudeCodeAgent(overrides Agent) Agent {
	return mergeAgent(Agent{Type: ClaudeCodeACPRegistryID, Command: "npm", Args: []string{"exec", "--yes", "@zed-industries/claude-agent-acp@0.23.1", "--"}}, overrides)
}

func CreateGeminiAgent(overrides Agent) Agent {
	return mergeAgent(Agent{Type: GeminiCLIACPRegistryID, Command: "gemini", Args: []string{"--experimental-acp"}}, overrides)
}

func CreateGitHubCopilotAgent(overrides Agent) Agent {
	return mergeAgent(Agent{Type: GitHubCopilotACPRegistryID, Command: "copilot", Args: []string{"acp"}}, overrides)
}

func CreateOpenCodeAgent(overrides Agent) Agent {
	return mergeAgent(Agent{Type: OpenCodeACPRegistryID, Command: "opencode", Args: []string{"acp"}}, overrides)
}

func CreatePiAgent(overrides Agent) Agent {
	return mergeAgent(Agent{Type: PiACPRegistryID, Command: "pi", Args: []string{"acp"}}, overrides)
}

func mergeAgent(base Agent, overrides Agent) Agent {
	if overrides.Type != "" {
		base.Type = overrides.Type
	}
	if overrides.Command != "" {
		base.Command = overrides.Command
	}
	if overrides.Args != nil {
		base.Args = overrides.Args
	}
	if overrides.Env != nil {
		base.Env = overrides.Env
	}
	return base
}
