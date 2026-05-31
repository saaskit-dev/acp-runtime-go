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
		CreateInitialConfigAliases:     func(key string, value any) []any { return []any{value} },
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
