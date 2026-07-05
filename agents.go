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
	return mergeAgent(Agent{Type: CodexACPRegistryID, Command: "npm", Args: []string{"exec", "--yes", "@zed-industries/codex-acp@0.16.0", "--"}}, overrides)
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
	// ExtraArgs from both base and overrides are appended to the final Args,
	// after any Args override. This lets callers add CLI flags (e.g.
	// --disallowedTools) without clobbering the agent's launch preamble.
	// Order: base.ExtraArgs first, then overrides.ExtraArgs.
	extra := append([]string(nil), base.ExtraArgs...)
	extra = append(extra, overrides.ExtraArgs...)
	if len(extra) > 0 {
		base.Args = append(append([]string(nil), base.Args...), extra...)
	}
	base.ExtraArgs = nil
	return base
}

// CreateClaudeCodeOptions builds the _meta.claudeCode.options object for the
// Claude Code ACP agent from a typed ClaudeCodeOptions value. Pass the returned
// map as StartSessionOptions.Meta to apply the configuration at session
// creation. Only non-empty/nil fields are emitted, so an empty ClaudeCodeOptions
// produces {"claudeCode":{"options":{}}}.
//
// Example — disable web tools:
//
//	meta := acp.CreateClaudeCodeOptions(acp.ClaudeCodeOptions{
//	    DisallowedTools: []string{"WebFetch", "WebSearch"},
//	})
//	runtime.StartSession(ctx, acp.StartSessionOptions{Agent: agent, Meta: meta})
//
// Example — strip all built-in tools (MCP-only):
//
//	meta := acp.CreateClaudeCodeOptions(acp.ClaudeCodeOptions{Tools: []string{}})
func CreateClaudeCodeOptions(opts ClaudeCodeOptions) map[string]any {
	options := map[string]any{}
	// Tools is emitted even when empty (len 0), because an empty array is the
	// signal to disable all built-in tools. Only omit when nil (unset).
	if opts.Tools != nil {
		if len(opts.Tools) == 0 {
			options["tools"] = []string{}
		} else {
			options["tools"] = opts.Tools
		}
	}
	if len(opts.DisallowedTools) > 0 {
		options["disallowedTools"] = opts.DisallowedTools
	}
	if len(opts.AllowedTools) > 0 {
		options["allowedTools"] = opts.AllowedTools
	}
	if opts.Settings != nil {
		options["settings"] = opts.Settings
	}
	return map[string]any{"claudeCode": map[string]any{"options": options}}
}
