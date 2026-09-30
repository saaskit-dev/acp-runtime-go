package acpruntime

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
)

// ConfigError identifies the source and field without echoing configuration
// values, which may include credentials in provider pass-through settings.
type ConfigError struct{ Source, Field, Reason string }

func (e *ConfigError) Error() string                 { return fmt.Sprintf("%s.%s: %s", e.Source, e.Field, e.Reason) }
func configError(source, field, reason string) error { return &ConfigError{source, field, reason} }

func parseConfigObject(data []byte, source string) (map[string]any, error) {
	var object map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&object); err != nil || object == nil {
		return nil, configError(source, "$", "expected a non-null JSON object")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, configError(source, "$", "expected exactly one JSON object")
	}
	return object, nil
}

func validateCodexConfig(config map[string]any) error {
	for key := range config {
		if key == "sandbox_mode" || key == "sandbox_workspace_write" || key == "approval_policy" {
			continue
		}
		for _, prefix := range []string{"sandbox", "approval", "permission", "writable_", "deny", "allow", "ask"} {
			if strings.HasPrefix(strings.ToLower(key), prefix) {
				return configError("CODEX_CONFIG", key, "unsupported security field")
			}
		}
	}
	for _, key := range []string{"model", "instructions", "developer_instructions", "model_reasoning_effort"} {
		if value, exists := config[key]; exists {
			if _, ok := value.(string); !ok {
				return configError("CODEX_CONFIG", key, "expected a string")
			}
		}
	}
	for key, allowed := range map[string][]string{"sandbox_mode": {"read-only", "workspace-write", "danger-full-access"}, "approval_policy": {"never", "on-request", "untrusted", "on-failure"}} {
		if value, exists := config[key]; exists {
			valid := false
			for _, candidate := range allowed {
				valid = valid || value == candidate
			}
			if !valid {
				return configError("CODEX_CONFIG", key, "unsupported value")
			}
		}
	}
	for _, key := range []string{"writable_roots", "permissions", "deny", "allow", "ask", "sandbox", "allowedTools", "disallowedTools"} {
		if _, exists := config[key]; exists {
			return configError("CODEX_CONFIG", key, "unsupported security field")
		}
	}
	if value, exists := config["sandbox_workspace_write"]; exists {
		nested, ok := value.(map[string]any)
		if !ok || nested == nil {
			return configError("CODEX_CONFIG", "sandbox_workspace_write", "expected a non-null object")
		}
		for key, value := range nested {
			switch key {
			case "writable_roots":
				if !isStringList(value) {
					return configError("CODEX_CONFIG", "sandbox_workspace_write."+key, "expected a string array")
				}
			case "network_access", "exclude_tmpdir_env_var", "exclude_slash_tmp":
				if _, ok := value.(bool); !ok {
					return configError("CODEX_CONFIG", "sandbox_workspace_write."+key, "expected a boolean")
				}
			default:
				return configError("CODEX_CONFIG", "sandbox_workspace_write."+key, "unsupported security field")
			}
		}
	}
	return nil
}
func isStringList(v any) bool {
	switch list := v.(type) {
	case []string:
		return true
	case []any:
		for _, item := range list {
			if _, ok := item.(string); !ok {
				return false
			}
		}
		return true
	default:
		return false
	}
}

// validateAgentStartConfig is called before projection so invalid input can
// never silently fall back to provider defaults.
func validateAgentStartConfig(agent Agent, cfg *AgentConfig, meta map[string]any) error {
	if cfg != nil {
		if _, err := json.Marshal(cfg.Extra); err != nil {
			return configError("AgentConfig", "Extra", "contains non-JSON values")
		}
	}
	if _, err := json.Marshal(meta); err != nil {
		return configError("Meta", "$", "contains non-JSON values")
	}
	switch agent.Type {
	case CodexACPRegistryID, CodexNativeRegistryID:
		if raw := agent.Env["CODEX_CONFIG"]; raw != "" {
			object, err := parseConfigObject([]byte(raw), "CODEX_CONFIG")
			if err != nil {
				return err
			}
			if err := validateCodexConfig(object); err != nil {
				return err
			}
		}
		if cfg != nil {
			if len(cfg.Permissions.Deny)+len(cfg.Permissions.Allow)+len(cfg.Permissions.Ask)+len(cfg.AllowedTools)+len(cfg.DisallowedTools) != 0 {
				return configError("AgentConfig", "permissions", "Codex cannot enforce unified tool/path permission rules; use an explicitly supported sandbox configuration")
			}
			if cfg.Sandbox != "" && cfg.Sandbox != "read-only" && cfg.Sandbox != "workspace-write" && cfg.Sandbox != "full-access" {
				return configError("AgentConfig", "sandbox", "unsupported sandbox")
			}
			if err := validateCodexConfig(cfg.Extra); err != nil {
				return err
			}
			if cfg.Sandbox != "" {
				desired := codexSandboxName(cfg.Sandbox)
				if extra, ok := cfg.Extra["sandbox_mode"]; ok && extra != desired {
					return configError("AgentConfig.Extra", "sandbox_mode", "conflicts with the requested sandbox")
				}
				if raw := agent.Env["CODEX_CONFIG"]; raw != "" {
					existing, _ := parseConfigObject([]byte(raw), "CODEX_CONFIG")
					if value, ok := existing["sandbox_mode"]; ok && value != desired {
						return configError("Agent.Env", "sandbox_mode", "conflicts with the requested sandbox")
					}
				}
				for _, arg := range agent.Args {
					if arg == "--yolo" || arg == "--full-auto" || arg == "-a" || strings.HasPrefix(arg, "--ask-for-approval") || strings.HasPrefix(arg, "--dangerously") || arg == "--sandbox" || arg == "-s" || strings.HasPrefix(arg, "--sandbox=") || strings.HasPrefix(arg, "sandbox_mode=") || strings.HasPrefix(arg, "sandbox_workspace_write.") || strings.HasPrefix(arg, "approval_policy=") {
						return configError("Agent.Args", "sandbox", "cannot combine sandbox argv overrides with AgentConfig.Sandbox")
					}
				}
			}
		}
	case ClaudeCodeACPRegistryID, ClaudeCodeNativeRegistryID:
		if cfg != nil && cfg.Sandbox != "" {
			return configError("AgentConfig", "sandbox", "Claude does not support this unified sandbox setting")
		}
		if err := validateClaudeMeta(meta); err != nil {
			return err
		}
		if cfg != nil {
			if err := validateClaudeSecurityOverrides(cfg, cfg.Extra, "AgentConfig.Extra"); err != nil {
				return err
			}
			cc, _ := meta["claudeCode"].(map[string]any)
			opts, _ := cc["options"].(map[string]any)
			if err := validateClaudeSecurityOverrides(cfg, opts, "Meta.claudeCode.options"); err != nil {
				return err
			}
		}
	}
	return nil
}
func validateClaudeMeta(meta map[string]any) error {
	value, exists := meta["claudeCode"]
	if !exists {
		return nil
	}
	cc, ok := value.(map[string]any)
	if !ok || cc == nil {
		return configError("Meta", "claudeCode", "expected a non-null object")
	}
	value, exists = cc["options"]
	if !exists {
		return nil
	}
	opts, ok := value.(map[string]any)
	if !ok || opts == nil {
		return configError("Meta", "claudeCode.options", "expected a non-null object")
	}
	for _, key := range []string{"tools", "allowedTools", "disallowedTools", "settingSources"} {
		if v, exists := opts[key]; exists && !isStringList(v) {
			return configError("Meta", "claudeCode.options."+key, "expected a string array")
		}
	}
	if value, exists := opts["settings"]; exists {
		settings, ok := value.(map[string]any)
		if !ok || settings == nil {
			return configError("Meta", "claudeCode.options.settings", "expected a non-null object")
		}
		if value, exists := settings["permissions"]; exists {
			permissions, ok := value.(map[string]any)
			if !ok || permissions == nil {
				return configError("Meta", "claudeCode.options.settings.permissions", "expected a non-null object")
			}
			for key, value := range permissions {
				switch key {
				case "allow", "deny", "ask":
					if !isStringList(value) {
						return configError("Meta", "permissions."+key, "expected a string array")
					}
				default:
					return configError("Meta", "permissions."+key, "unsupported security field")
				}
			}
		}
	}
	return nil
}

// resolveNativeInitialConfig runs before session/new or session/resume. A
// selected InitialConfig overrides the projected base AgentConfig; explicit
// Meta/argv conflicts must be rejected by the caller before this projection.
func resolveNativeInitialConfig(agent Agent, meta map[string]any, initial InitialConfig) (Agent, map[string]any, error) {
	if agent.Type != CodexNativeRegistryID && agent.Type != ClaudeCodeNativeRegistryID {
		return agent, meta, nil
	}
	meta = mergeSessionMeta(nil, meta)
	for _, key := range []string{"model", "mode", "effort"} {
		if value, exists := meta[key]; exists {
			text, ok := value.(string)
			if !ok || strings.TrimSpace(text) == "" {
				return agent, nil, configError("Meta", key, "expected a nonempty string")
			}
			if key == "effort" || (key == "mode" && agent.Type == CodexNativeRegistryID) {
				return agent, nil, configError("Meta", key, "unsupported native start option")
			}
		}
	}
	for key, value := range map[string]any{"model": initial.Model, "mode": initial.Mode, "effort": initial.Effort} {
		if value == nil {
			continue
		}
		text, ok := value.(string)
		if !ok || strings.TrimSpace(text) == "" {
			return agent, nil, configError("InitialConfig", key, "expected a nonempty string")
		}
		if key == "effort" || (key == "mode" && agent.Type == CodexNativeRegistryID) {
			return agent, nil, configError("InitialConfig", key, "unsupported native start option")
		}
		if key == "mode" && text == "yolo" {
			text = "bypassPermissions"
		}
		meta[key] = text
	}
	for key := range initial.Raw {
		return agent, nil, configError("InitialConfig.Raw", key, "unsupported native start option")
	}
	if agent.Type == ClaudeCodeNativeRegistryID {
		if mode := metaString(meta, "mode"); mode != "" {
			switch mode {
			case "default", "acceptEdits", "plan", "bypassPermissions", "dontAsk":
			default:
				return agent, nil, configError("Meta", "mode", "unsupported permission mode")
			}
		}
		if err := validateClaudeNativeMeta(meta); err != nil {
			return agent, nil, err
		}
	}
	if err := validateNativeArgs(agent, meta, initial); err != nil {
		return agent, nil, err
	}
	return agent, meta, nil
}

func validateNativeArgs(agent Agent, meta map[string]any, initial InitialConfig) error {
	if agent.Type != CodexNativeRegistryID && agent.Type != ClaudeCodeNativeRegistryID {
		return nil
	}
	for index, arg := range agent.Args {
		key, value := "", ""
		switch {
		case arg == "--model" || arg == "-m":
			key = "model"
			if index+1 < len(agent.Args) {
				value = agent.Args[index+1]
			}
		case strings.HasPrefix(arg, "--model="):
			key = "model"
			value = strings.TrimPrefix(arg, "--model=")
		case arg == "--permission-mode":
			key = "mode"
			if index+1 < len(agent.Args) {
				value = agent.Args[index+1]
			}
		case strings.HasPrefix(arg, "--permission-mode="):
			key = "mode"
			value = strings.TrimPrefix(arg, "--permission-mode=")
		}
		if key != "" {
			if selected := metaString(meta, key); selected != "" && value != selected {
				return configError("Agent.Args", key, "conflicts with the resolved start option")
			}
		}
	}
	return nil
}

func validateClaudeNativeMeta(meta map[string]any) error {
	if err := validateClaudeMeta(meta); err != nil {
		return err
	}
	cc, _ := meta["claudeCode"].(map[string]any)
	options, _ := cc["options"].(map[string]any)
	for key := range options {
		switch key {
		case "tools", "allowedTools", "disallowedTools", "settingSources", "settings", "plugins":
		default:
			return configError("Meta", "claudeCode.options."+key, "unsupported native option")
		}
	}
	if value, exists := options["plugins"]; exists {
		list, ok := value.([]any)
		if !ok {
			return configError("Meta", "claudeCode.options.plugins", "expected an array of local plugins")
		}
		for _, value := range list {
			plugin, ok := value.(map[string]any)
			if !ok || plugin["type"] != "local" {
				return configError("Meta", "claudeCode.options.plugins", "unsupported plugin entry")
			}
			path, ok := plugin["path"].(string)
			if !ok || strings.TrimSpace(path) == "" {
				return configError("Meta", "claudeCode.options.plugins.path", "expected a nonempty string")
			}
		}
	}
	return nil
}

func validateClaudeSecurityOverrides(cfg *AgentConfig, options map[string]any, source string) error {
	for key, rules := range map[string][]string{"allowedTools": cfg.AllowedTools, "disallowedTools": cfg.DisallowedTools} {
		if override, exists := options[key]; len(rules) > 0 && exists && !reflect.DeepEqual(stringSliceFromAny(override), rules) {
			return configError(source, key, "conflicts with requested tool policy")
		}
	}
	settings, _ := options["settings"].(map[string]any)
	permissions, _ := settings["permissions"].(map[string]any)
	for key, rules := range map[string][]string{"allow": cfg.Permissions.Allow, "deny": cfg.Permissions.Deny, "ask": cfg.Permissions.Ask} {
		if override, exists := permissions[key]; len(rules) > 0 && exists && !reflect.DeepEqual(stringSliceFromAny(override), rules) {
			return configError(source, "settings.permissions."+key, "conflicts with requested permission policy")
		}
	}
	return nil
}
