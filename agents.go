package acpruntime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

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

// CreateCodexAgent builds an Agent that launches the Codex ACP wrapper via npx.
// The package is unpinned so npm resolves the latest published version each
// spawn, keeping the wrapper in sync with upstream without a code change.
func CreateCodexAgent(overrides Agent) Agent {
	return mergeAgent(Agent{Type: CodexACPRegistryID, Command: "npm", Args: []string{"exec", "--yes", "@agentclientprotocol/codex-acp", "--"}}, overrides)
}

// CreateClaudeCodeAgent builds an Agent that launches the Claude Code ACP
// wrapper via npx. The package is unpinned so npm resolves the latest published
// version each spawn, keeping the wrapper in sync with upstream without a code
// change.
func CreateClaudeCodeAgent(overrides Agent) Agent {
	return mergeAgent(Agent{Type: ClaudeCodeACPRegistryID, Command: "npm", Args: []string{"exec", "--yes", "@agentclientprotocol/claude-agent-acp", "--"}}, overrides)
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

// CreateCodexConfig builds the CODEX_CONFIG env value (JSON) from a typed
// CodexConfig. Returns a map suitable for Agent.Env. If agentEnv already has a
// CODEX_CONFIG, the values are deep-merged (new values win).
//
// System prompts are not configured here — use WithSystemPrompt /
// WithAppendSystemPrompt on StartSessionOptions.Meta.
//
// Example:
//
//	env, _ := acp.CreateCodexConfig(acp.CodexConfig{SandboxMode: "read-only"})
//	agent := acp.CreateCodexAgent(acp.Agent{Env: env})
func CreateCodexConfig(opts CodexConfig) (map[string]string, error) {
	return buildCodexEnv(opts, nil)
}

// buildCodexEnv constructs or merges the CODEX_CONFIG env map from CodexConfig
// options. existingEnv provides the base env (may contain a prior CODEX_CONFIG
// that gets deep-merged).
func buildCodexEnv(opts CodexConfig, existingEnv map[string]string) (map[string]string, error) {
	env := map[string]string{}
	for k, v := range existingEnv {
		env[k] = v
	}
	config := map[string]any{}
	// Parse existing CODEX_CONFIG if present, so we merge rather than clobber.
	if existing := env["CODEX_CONFIG"]; existing != "" {
		var err error
		config, err = parseConfigObject([]byte(existing), "CODEX_CONFIG")
		if err != nil {
			return nil, err
		}
	}
	if opts.Model != "" {
		config["model"] = opts.Model
	}
	if opts.SandboxMode != "" {
		config["sandbox_mode"] = opts.SandboxMode
	}
	if opts.ApprovalPolicy != "" {
		config["approval_policy"] = opts.ApprovalPolicy
	}
	if len(opts.WritableRoots) > 0 || opts.NetworkAccess != nil {
		nested := map[string]any{}
		if value, exists := config["sandbox_workspace_write"]; exists {
			var ok bool
			nested, ok = value.(map[string]any)
			if !ok || nested == nil {
				return nil, configError("CODEX_CONFIG", "sandbox_workspace_write", "expected a non-null object")
			}
		}
		if len(opts.WritableRoots) > 0 {
			nested["writable_roots"] = append([]string(nil), opts.WritableRoots...)
		}
		if opts.NetworkAccess != nil {
			nested["network_access"] = *opts.NetworkAccess
		}
		config["sandbox_workspace_write"] = nested
	}
	config = mergeSessionMeta(config, opts.Extra)
	if err := validateCodexConfig(config); err != nil {
		return nil, err
	}
	data, err := json.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("marshal CODEX_CONFIG: %w", err)
	}
	env["CODEX_CONFIG"] = string(data)
	return env, nil
}

// WriteOpenCodeConfig writes an opencode.json file to the given directory. Because
// OpenCode does not read _meta or env for permission/tool configuration, the
// file is the only reliable way to restrict tools. Call this before StartSession
// with the same CWD.
//
// Example:
//
//	acp.WriteOpenCodeConfig(cwd, acp.OpenCodeConfig{
//	    Permission: acp.OpenCodePermission{Deny: []string{"bash"}},
//	})
//	session, _ := runtime.StartSession(ctx, acp.StartSessionOptions{Agent: agent, CWD: cwd})
var openCodeConfigWriteMu sync.Mutex

func WriteOpenCodeConfig(cwd string, opts OpenCodeConfig) error {
	openCodeConfigWriteMu.Lock()
	defer openCodeConfigWriteMu.Unlock()
	config := map[string]any{}
	// Preserve existing opencode.json if present.
	existingPath := filepath.Join(cwd, "opencode.json")
	mode := os.FileMode(0o644)
	if data, err := os.ReadFile(existingPath); err == nil {
		config, err = parseConfigObject(data, "opencode.json")
		if err != nil {
			return err
		}
		stat, err := os.Stat(existingPath)
		if err != nil {
			return err
		}
		mode = stat.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return err
	}
	if opts.Model != "" {
		config["model"] = opts.Model
	}
	if opts.Provider != "" {
		config["provider"] = opts.Provider
	}
	if len(opts.Permission.Allow) > 0 || len(opts.Permission.Deny) > 0 || len(opts.Permission.Ask) > 0 {
		perm := map[string]any{}
		if len(opts.Permission.Allow) > 0 {
			perm["allow"] = opts.Permission.Allow
		}
		if len(opts.Permission.Deny) > 0 {
			perm["deny"] = opts.Permission.Deny
		}
		if len(opts.Permission.Ask) > 0 {
			perm["ask"] = opts.Permission.Ask
		}
		if existing, exists := config["permission"]; exists {
			object, ok := existing.(map[string]any)
			if !ok || object == nil {
				return configError("opencode.json", "permission", "expected a non-null object")
			}
			perm = mergeSessionMeta(object, perm)
		}
		config["permission"] = perm
	}
	config = mergeSessionMeta(config, opts.Extra)
	if permission, exists := config["permission"]; exists {
		object, ok := permission.(map[string]any)
		if !ok || object == nil {
			return configError("opencode.json", "permission", "expected a non-null object")
		}
		for _, key := range []string{"allow", "deny", "ask"} {
			if value, exists := object[key]; exists && !isStringList(value) {
				return configError("opencode.json", "permission."+key, "expected a string array")
			}
		}
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal opencode.json: %w", err)
	}
	data = append(data, '\n')
	file, err := os.CreateTemp(cwd, ".opencode-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err := file.Chmod(mode); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), existingPath)
}
