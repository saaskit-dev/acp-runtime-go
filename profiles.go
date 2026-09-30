package acpruntime

import (
	"encoding/json"
	"strings"
)

type AgentProfile struct {
	ProjectSystemPrompt               func(Agent, SystemPromptProjection) (Agent, map[string]any)
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
	case CodexACPRegistryID, CodexNativeRegistryID:
		profile.NormalizeRuntimeAuthMethods = func(agent Agent, methods []RuntimeAuthenticationMethod) []RuntimeAuthenticationMethod {
			return methods
		}
		// codex-acp ignores session/new _meta.systemPrompt. Host intent is
		// projected through CODEX_CONFIG (ConfigToml keys merged into session
		// config): replace → instructions (system/base override), append →
		// developer_instructions.
		profile.ProjectSystemPrompt = projectCodexSystemPrompt
		profile.ApplyAgentConfig = applyCodexAgentConfig
	case ClaudeCodeACPRegistryID:
		profile.CreateInitialConfigAliases = func(key string, value any) []any {
			if key == "mode" && value == "yolo" {
				return []any{"bypassPermissions", value}
			}
			return []any{value}
		}
		// claude-agent-acp (Agent SDK wrapper) reads system prompt from
		// session/new _meta.systemPrompt, not from process CLI flags.
		// String value = replace; object with preset+append = append to the
		// built-in claude_code prompt. Strip any stale --system-prompt*
		// args so callers cannot double-apply via Agent.Args.
		profile.ProjectSystemPrompt = func(agent Agent, prompt SystemPromptProjection) (Agent, map[string]any) {
			agent.Args = removeClaudeSystemPromptArgs(agent.Args)
			if prompt.Mode == SystemPromptModeAppend {
				return agent, map[string]any{
					SystemPromptMetaKey: map[string]any{
						"type":   "preset",
						"preset": "claude_code",
						"append": prompt.Text,
					},
				}
			}
			return agent, map[string]any{SystemPromptMetaKey: prompt.Text}
		}
		profile.ApplyAgentConfig = applyClaudeAgentConfig
	case ClaudeCodeNativeRegistryID:
		// Same unified-config projection as the ACP path: AgentConfig lands in
		// _meta.claudeCode.options, which the native adapter translates into
		// spawn flags (allowedTools/disallowedTools/settings). The yolo mode
		// alias matches the ACP behavior.
		basePromptProjection := profile.ProjectSystemPrompt
		profile.ProjectSystemPrompt = func(agent Agent, prompt SystemPromptProjection) (Agent, map[string]any) {
			agent.Args = removeClaudeSystemPromptArgs(agent.Args)
			return basePromptProjection(agent, prompt)
		}
		profile.ApplyAgentConfig = func(agent Agent, cfg AgentConfig) (Agent, map[string]any) {
			agent, meta := applyClaudeAgentConfig(agent, cfg)
			if cfg.Model != "" {
				meta = mergeSessionMeta(meta, map[string]any{"model": cfg.Model})
			}
			return agent, meta
		}
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
	case OpenCodeACPRegistryID:
		profile.ApplyAgentConfig = applyOpenCodeAgentConfig
	}
	return profile
}

func defaultAgentProfile() AgentProfile {
	return AgentProfile{
		NormalizeInitializeAuthMethods: func(agent Agent, methods []AuthMethod) []AuthMethod { return methods },
		NormalizeRuntimeAuthMethods:    func(agent Agent, methods []RuntimeAuthenticationMethod) []RuntimeAuthenticationMethod { return methods },
		// The default projection preserves the public logical _meta contract on
		// the ACP wire. Provider profiles may consume it and choose a different
		// native transport, but must project it exactly once.
		ProjectSystemPrompt: func(agent Agent, prompt SystemPromptProjection) (Agent, map[string]any) {
			key := SystemPromptMetaKey
			if prompt.Mode == SystemPromptModeAppend {
				key = AppendSystemPromptMetaKey
			}
			return agent, map[string]any{key: prompt.Text}
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
			case "read", "search", "read_file":
				return "read_file"
			case "edit", "delete", "move", "write_file":
				return "write_file"
			case "execute", "execute_command":
				return "execute_command"
			case "fetch", "network_request":
				return "network_request"
			case "mcp_call":
				return "mcp_call"
			default:
				return "unknown"
			}
		},
	}
}

func removeClaudeSystemPromptArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--system-prompt" || arg == "--append-system-prompt" {
			if i+1 < len(args) {
				i++
			}
			continue
		}
		if strings.HasPrefix(arg, "--system-prompt=") || strings.HasPrefix(arg, "--append-system-prompt=") {
			continue
		}
		out = append(out, arg)
	}
	return out
}

// projectCodexSystemPrompt projects host system-prompt intent into
// Agent.Env["CODEX_CONFIG"]. Returns nil session meta because codex-acp does
// not consume ACP _meta prompt keys.
//
// ConfigToml keys (what CODEX_CONFIG must use):
//
//	replace → instructions            (system/base override; becomes Config.base_instructions)
//	append  → developer_instructions  (separate developer-layer message)
func projectCodexSystemPrompt(agent Agent, prompt SystemPromptProjection) (Agent, map[string]any) {
	env, err := injectCodexSystemPromptConfig(agent.Env, prompt)
	if err != nil {
		return agent, nil
	}
	agent.Env = env
	return agent, nil
}

// CODEX_CONFIG / ConfigToml keys. Base override is "instructions", not
// "base_instructions" — the latter is only the resolved Config field name.
const (
	codexConfigInstructionsKey          = "instructions"
	codexConfigDeveloperInstructionsKey = "developer_instructions"
)

// injectCodexSystemPromptConfig deep-merges the host prompt into CODEX_CONFIG.
// Replace overwrites instructions; append concatenates onto any existing
// developer_instructions. The two config fields are independent layers.
func injectCodexSystemPromptConfig(existingEnv map[string]string, prompt SystemPromptProjection) (map[string]string, error) {
	env := map[string]string{}
	for k, v := range existingEnv {
		env[k] = v
	}
	config := map[string]any{}
	if existing := env["CODEX_CONFIG"]; existing != "" {
		var err error
		config, err = parseConfigObject([]byte(existing), "CODEX_CONFIG")
		if err != nil {
			return nil, err
		}
		if err := validateCodexConfig(config); err != nil {
			return nil, err
		}
	}
	text := strings.TrimSpace(prompt.Text)
	if text == "" {
		return env, nil
	}
	if prompt.Mode == SystemPromptModeAppend {
		if prev, ok := config[codexConfigDeveloperInstructionsKey].(string); ok {
			prev = strings.TrimSpace(prev)
			if prev != "" {
				text = prev + "\n\n" + text
			}
		}
		config[codexConfigDeveloperInstructionsKey] = text
	} else {
		config[codexConfigInstructionsKey] = text
	}
	data, err := json.Marshal(config)
	if err != nil {
		return nil, err
	}
	env["CODEX_CONFIG"] = string(data)
	return env, nil
}

// applyClaudeAgentConfig translates AgentConfig into Claude Code's native format:
// _meta.claudeCode.options (disallowedTools, allowedTools, settings.permissions).
// Model reaches the wrapper SDK options (ACP) or resolved spawn metadata (native).
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
	if cfg.Model != "" {
		if agent.Type == ClaudeCodeACPRegistryID {
			mergeExtraIntoClaudeOptions(meta, map[string]any{"model": cfg.Model})
		} else {
			meta["model"] = cfg.Model
		}
	}
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
	} else if cfg.Sandbox == "full-access" {
		codexOpts.ApprovalPolicy = "never"
	}
	codexOpts.Extra = cfg.Extra
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
	cc["options"] = mergeSessionMeta(options, extra)
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
