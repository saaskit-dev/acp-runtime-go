package acpruntime

import "strings"

type AgentProfile struct {
	ApplySystemPromptToAgent          func(Agent, SystemPrompt) Agent
	CreateSystemPromptSessionMeta     func(SystemPrompt) map[string]any
	NormalizeInitializeAuthMethods    func(Agent, []AuthMethod) []AuthMethod
	NormalizeRuntimeAuthMethods       func(Agent, []RuntimeAuthenticationMethod) []RuntimeAuthenticationMethod
	CreateInitialConfigAliases        func(key string, value any) []any
	CreateInitialConfigOptionSelector func(key string) InitialConfigOptionSelector
	MapOperationKind                  func(kind string) string
}

type InitialConfigOptionSelector struct {
	Categories []string
	IDs        []string
}

func ResolveAgentProfile(agent Agent) AgentProfile {
	profile := defaultAgentProfile()
	switch agent.Type {
	case CodexACPRegistryID:
		profile.NormalizeRuntimeAuthMethods = func(agent Agent, methods []RuntimeAuthenticationMethod) []RuntimeAuthenticationMethod {
			return methods
		}
	case ClaudeCodeACPRegistryID:
		profile.CreateInitialConfigAliases = func(key string, value any) []any {
			if key == "mode" && value == "yolo" {
				return []any{"bypassPermissions", value}
			}
			return []any{value}
		}
		// Claude Code's ACP adapter accepts --append-system-prompt, which
		// appends to (rather than replaces) its built-in system prompt. We
		// inject the flag into the agent's CLI args so the host's system prompt
		// reaches Claude Code without clobbering its defaults. Combined with
		// the default CreateSystemPromptSessionMeta this also covers agents
		// that read _meta.systemPrompt directly.
		profile.ApplySystemPromptToAgent = func(agent Agent, prompt SystemPrompt) Agent {
			if strings.TrimSpace(prompt.Text) == "" {
				return agent
			}
			args := make([]string, 0, len(agent.Args)+2)
			args = append(args, agent.Args...)
			args = append(args, "--append-system-prompt", prompt.Text)
			agent.Args = args
			return agent
		}
	case GitHubCopilotACPRegistryID:
		profile.NormalizeInitializeAuthMethods = func(agent Agent, methods []AuthMethod) []AuthMethod {
			if len(methods) > 0 {
				return methods
			}
			return []AuthMethod{{Type: "agent", ID: "github-copilot-login", Name: "GitHub Copilot"}}
		}
	case LocalSimulatorAgentACPRegistryID, SimulatorAgentACPRegistryID:
		profile.NormalizeInitializeAuthMethods = func(agent Agent, methods []AuthMethod) []AuthMethod {
			return methods
		}
	}
	return profile
}

func defaultAgentProfile() AgentProfile {
	return AgentProfile{
		NormalizeInitializeAuthMethods: func(agent Agent, methods []AuthMethod) []AuthMethod { return methods },
		NormalizeRuntimeAuthMethods:    func(agent Agent, methods []RuntimeAuthenticationMethod) []RuntimeAuthenticationMethod { return methods },
		// CreateSystemPromptSessionMeta forwards the host's system prompt to the
		// agent via session/new _meta.systemPrompt. This is a community
		// convention (used by the Zed claude/codex ACP adapters) rather than a
		// formal ACP v1 field; agents that read _meta.systemPrompt will pick it
		// up, others ignore it. An empty prompt yields no meta so we never
		// clobber an agent's own system prompt with an empty string.
		CreateSystemPromptSessionMeta: func(prompt SystemPrompt) map[string]any {
			if strings.TrimSpace(prompt.Text) == "" {
				return nil
			}
			return map[string]any{"systemPrompt": prompt.Text}
		},
		CreateInitialConfigAliases: func(key string, value any) []any { return []any{value} },
		CreateInitialConfigOptionSelector: func(key string) InitialConfigOptionSelector {
			switch key {
			case "mode":
				return InitialConfigOptionSelector{Categories: []string{"mode"}, IDs: []string{"mode"}}
			case "model":
				return InitialConfigOptionSelector{Categories: []string{"model"}, IDs: []string{"model"}}
			case "effort":
				return InitialConfigOptionSelector{Categories: []string{"effort", "thought_level"}, IDs: []string{"effort", "reasoning_effort"}}
			default:
				return InitialConfigOptionSelector{IDs: []string{key}}
			}
		},
		MapOperationKind: func(kind string) string {
			switch strings.ToLower(kind) {
			case "read", "search":
				return "read_file"
			case "edit", "delete", "move":
				return "write_file"
			case "execute":
				return "execute_command"
			case "fetch":
				return "network_request"
			case "":
				return "unknown"
			default:
				return "mcp_call"
			}
		},
	}
}

func runtimeAuthMethodsFromACP(methods []AuthMethod) []RuntimeAuthenticationMethod {
	out := make([]RuntimeAuthenticationMethod, 0, len(methods))
	for _, method := range methods {
		methodType := method.Type
		if methodType == "" {
			methodType = "agent"
		}
		description := ""
		if method.Description != nil {
			description = *method.Description
		}
		link := ""
		if method.Link != nil {
			link = *method.Link
		}
		out = append(out, RuntimeAuthenticationMethod{
			Type:        methodType,
			ID:          method.ID,
			Name:        method.Name,
			Description: description,
			Link:        link,
			Vars:        method.Vars,
			Args:        method.Args,
			Env:         method.Env,
			Meta:        method.Meta,
		})
	}
	return out
}

func selectRuntimeAuthenticationMethod(methods []RuntimeAuthenticationMethod) (RuntimeAuthenticationMethod, bool) {
	if len(methods) == 0 {
		return RuntimeAuthenticationMethod{}, false
	}
	for _, method := range methods {
		if method.Type == "agent" || method.Type == "" {
			return method, true
		}
	}
	return methods[0], true
}
