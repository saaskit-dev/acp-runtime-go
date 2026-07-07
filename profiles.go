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
	// ApplyAgentConfig translates a unified AgentConfig into the agent's native
	// format. Returns a potentially-modified Agent (e.g. with injected env/args)
	// and optional session/new _meta. Called during Create() before the explicit
	// Meta merge. nil = no translation (AgentConfig fields are ignored for that
	// agent type, except via best-effort InitialConfig).
	ApplyAgentConfig func(Agent, AgentConfig) (Agent, map[string]any)
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
		profile.ApplyAgentConfig = applyCodexAgentConfig
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
		profile.ApplyAgentConfig = applyClaudeAgentConfig
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
	case OpenCodeACPRegistryID:
		profile.ApplyAgentConfig = applyOpenCodeAgentConfig
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

// applyClaudeAgentConfig translates AgentConfig into Claude Code's native format:
// _meta.claudeCode.options (disallowedTools, allowedTools, settings.permissions).
// Model is handled separately via InitialConfig (the standard ACP config option).
func applyClaudeAgentConfig(agent Agent, cfg AgentConfig) (Agent, map[string]any) {
	opts := ClaudeCodeOptions{
		DisallowedTools: cfg.DisallowedTools,
		AllowedTools:    cfg.AllowedTools,
	}
	if len(cfg.Permissions.Deny) > 0 || len(cfg.Permissions.Allow) > 0 || len(cfg.Permissions.Ask) > 0 {
		perm := map[string]any{}
		if len(cfg.Permissions.Allow) > 0 {
			perm["allow"] = cfg.Permissions.Allow
		}
		if len(cfg.Permissions.Deny) > 0 {
			perm["deny"] = cfg.Permissions.Deny
		}
		if len(cfg.Permissions.Ask) > 0 {
			perm["ask"] = cfg.Permissions.Ask
		}
		opts.Settings = map[string]any{"permissions": perm}
	}
	meta := CreateClaudeCodeOptions(opts)
	// Extra fields go into claudeCode.options directly.
	if len(cfg.Extra) > 0 {
		mergeExtraIntoClaudeOptions(meta, cfg.Extra)
	}
	return agent, meta
}

// applyCodexAgentConfig translates AgentConfig into Codex's native format:
// CODEX_CONFIG env JSON (sandbox_mode, approval_policy, model). Returns a
// modified Agent with the env injected. No _meta (codex doesn't read it).
func applyCodexAgentConfig(agent Agent, cfg AgentConfig) (Agent, map[string]any) {
	codexOpts := CodexConfig{}
	if cfg.Model != "" {
		codexOpts.Model = cfg.Model
	}
	if cfg.Sandbox != "" {
		codexOpts.SandboxMode = codexSandboxName(cfg.Sandbox)
	}
	if cfg.Sandbox == "read-only" {
		codexOpts.ApprovalPolicy = "on-request"
	}
	if len(cfg.Permissions.Deny) > 0 {
		codexOpts.WritableRoots = filterWritableRoots(cfg.Permissions.Deny)
	}
	env, err := buildCodexEnv(codexOpts, agent.Env)
	if err != nil {
		return agent, nil // best-effort: skip on JSON error
	}
	agent.Env = env
	return agent, nil
}

// applyOpenCodeAgentConfig translates AgentConfig for OpenCode. Model goes via
// _meta (OpenCode reads it via session config options). Permissions require a
// opencode.json file (use WriteOpenCodeConfig separately, since the profile hook
// has no CWD context to write to).
func applyOpenCodeAgentConfig(agent Agent, cfg AgentConfig) (Agent, map[string]any) {
	meta := map[string]any{}
	if cfg.Model != "" {
		meta["model"] = cfg.Model
	}
	if len(cfg.Extra) > 0 {
		for k, v := range cfg.Extra {
			meta[k] = v
		}
	}
	if len(meta) == 0 {
		return agent, nil
	}
	return agent, meta
}

// codexSandboxName maps the unified sandbox names to Codex's native names.
func codexSandboxName(unified string) string {
	switch unified {
	case "read-only":
		return "read-only"
	case "workspace-write":
		return "workspace-write"
	case "full-access":
		return "danger-full-access"
	default:
		return unified
	}
}

// filterWritableRoots extracts path-like entries from a deny list for Codex's
// writable_roots (best-effort: only entries containing "/" are treated as paths).
func filterWritableRoots(deny []string) []string {
	var roots []string
	for _, d := range deny {
		if strings.Contains(d, "/") {
			roots = append(roots, d)
		}
	}
	return roots
}

// mergeExtraIntoClaudeOptions deep-merges extra fields into the
// _meta.claudeCode.options map.
func mergeExtraIntoClaudeOptions(meta map[string]any, extra map[string]any) {
	cc, ok := meta["claudeCode"].(map[string]any)
	if !ok {
		return
	}
	options, ok := cc["options"].(map[string]any)
	if !ok {
		return
	}
	for k, v := range extra {
		options[k] = v
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
