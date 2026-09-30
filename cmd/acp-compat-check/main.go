// Command acp-compat-check runs credential-free contract checks by default.
// Live wrapper/native suites are explicit and cache only successful evidence
// bound to the full runtime, schema, fixture, configuration and engine identity.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	acp "github.com/saaskit-dev/acp-runtime-go"
)

// sentinelToken is the exact string we ask the agent to echo back. If the
// agent returns it, the full spawn -> initialize -> session/new -> prompt ->
// output chain is confirmed working.
const sentinelToken = "COMPAT_OK"

// Gateway env vars. When both are set, the check runs each agent through the
// unified router gateway (a single base URL + key) instead of requiring
// separate ANTHROPIC_API_KEY / OPENAI_API_KEY secrets.
const (
	gatewayBaseURLEnv = "UNIFIED_ROUTER_BASE_URL"
	gatewayKeyEnv     = "UNIFIED_ROUTER_KEY"
)

type agentCheck struct {
	name      string                             // human label
	pkg       string                             // npm package name for version query + cache key
	buildFunc func() (acp.Agent, map[string]any) // agent + optional session/new _meta
	apiKeyEnv string                             // env var that must be present to run the real test
	// localVersion, when set, sources the version from the LOCAL CLI instead
	// of npm (native transports drive the user's own binary).
	localVersion func() (string, error)
	// localAuth marks engines that authenticate through their own CLI login
	// (no provider API key env is required).
	localAuth   bool
	localBinary string
}

func main() { os.Exit(runMain(os.Args[1:])) }

func availableChecks() []agentCheck {
	return []agentCheck{
		{name: "claude-agent-acp", pkg: "@agentclientprotocol/claude-agent-acp", buildFunc: buildClaudeAgent, apiKeyEnv: "ANTHROPIC_API_KEY"},
		{name: "codex-acp", pkg: "@agentclientprotocol/codex-acp", buildFunc: buildCodexAgent, apiKeyEnv: "OPENAI_API_KEY"},
		{name: "codex-native", pkg: "native:codex", buildFunc: buildNativeCodexAgent, localVersion: func() (string, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			return acp.ProbeNativeEngineVersion(ctx, "codex")
		}, localAuth: true, localBinary: "codex"},
		{name: "claude-native", pkg: "native:claude", buildFunc: buildNativeClaudeAgent, localVersion: func() (string, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			return acp.ProbeNativeEngineVersion(ctx, "claude")
		}, localAuth: true, localBinary: "claude"},
	}
}

// version resolves the engine version: local CLI probe for native engines,
// npm latest for wrapper packages.
func (c agentCheck) version() (string, error) {
	if c.localVersion != nil {
		return c.localVersion()
	}
	return npmLatestVersion(c.pkg)
}

// canRun reports whether credentials exist for this engine: local-login
// engines require their CLI on PATH; wrapper engines need a key or gateway.
func (c agentCheck) canRun() bool {
	if c.localAuth {
		_, err := exec.LookPath(c.localBinary)
		return err == nil
	}
	return canRunAgent(c.apiKeyEnv)
}

// buildNativeCodexAgent constructs the codex native transport agent. The CLI
// carries its own login; CODEX_NATIVE_TEST_HOME optionally redirects
// CODEX_HOME (sandboxed/CI runs point it at a prepared copy).
func buildNativeCodexAgent() (acp.Agent, map[string]any) {
	agent := acp.CreateCodexNativeAgent(acp.Agent{})
	if home := os.Getenv("CODEX_NATIVE_TEST_HOME"); home != "" {
		agent.Env = map[string]string{"CODEX_HOME": home}
	}
	return agent, nil
}

// buildNativeClaudeAgent constructs the claude native transport agent.
func buildNativeClaudeAgent() (acp.Agent, map[string]any) {
	return acp.CreateClaudeCodeNativeAgent(acp.Agent{}), nil
}

// cacheFilePath returns the path to the version cache file. It honors the
// COMPAT_CACHE env var so CI can point it at a persisted artifact, and defaults
// to .compat-versions.json in the working directory.
func cacheFilePath() string {
	if p := os.Getenv("COMPAT_CACHE"); p != "" {
		return p
	}
	return ".compat-versions.json"
}

// npmLatestVersion queries `npm view <pkg> version` and returns the trimmed
// latest version string.
func npmLatestVersion(pkg string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "npm", "view", pkg, "version").Output()
	if err != nil {
		return "", err
	}
	version := strings.TrimSpace(string(out))
	if !regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`).MatchString(version) {
		return "", fmt.Errorf("npm returned no exact semantic version")
	}
	return version, nil
}

// apiKeyPresent checks whether the given env var (or a documented fallback)
// is non-empty.
func apiKeyPresent(primary string) bool {
	if os.Getenv(primary) != "" {
		return true
	}
	// codex accepts CODEX_API_KEY as an alternative to OPENAI_API_KEY.
	if primary == "OPENAI_API_KEY" && os.Getenv("CODEX_API_KEY") != "" {
		return true
	}
	return false
}

// gatewayConfigured reports whether the unified router gateway env vars are
// both set, enabling single-secret testing of both claude and codex agents.
func gatewayConfigured() bool {
	return os.Getenv(gatewayBaseURLEnv) != "" && os.Getenv(gatewayKeyEnv) != ""
}

// canRunAgent reports whether we have credentials to test this agent: either
// the agent-specific API key, or the gateway configured (which covers both).
func canRunAgent(apiKeyEnv string) bool {
	return apiKeyPresent(apiKeyEnv) || gatewayConfigured()
}

// buildClaudeAgent constructs a claude-agent-acp Agent + optional _meta for
// gateway mode. Three issues must be solved for the Claude Agent SDK to work
// through a non-Anthropic gateway:
//   - ANTHROPIC_BASE_URL must NOT include /v1 (the SDK appends /v1/messages).
//   - Without OAuth the SDK uses a full model name (claude-opus-4-8) the gateway
//     doesn't have. ANTHROPIC_CUSTOM_MODEL_OPTION (supported since claude-acp
//     v0.45.0, PR #768) registers the gateway model in the SDK's catalog, then
//     settings.model selects it. This avoids the [1m] suffix issue because the
//     SDK doesn't add context markers to non-Claude models.
func buildClaudeAgent() (acp.Agent, map[string]any) {
	agent := acp.CreateClaudeCodeAgent(acp.Agent{})
	if !gatewayConfigured() {
		return agent, nil
	}
	model := os.Getenv("CLAUDE_GATEWAY_MODEL")
	if model == "" {
		model = "glm-5.2"
	}
	agent.Env = map[string]string{
		"ANTHROPIC_BASE_URL":            os.Getenv(gatewayBaseURLEnv), // no /v1
		"ANTHROPIC_API_KEY":             os.Getenv(gatewayKeyEnv),
		"ANTHROPIC_CUSTOM_MODEL_OPTION": model,
	}
	meta := acp.CreateClaudeCodeOptions(acp.ClaudeCodeOptions{
		Settings: map[string]any{"model": model},
	})
	return agent, meta
}

// buildCodexAgent constructs a codex-acp Agent + optional _meta for gateway
// mode. Codex's CODEX_CONFIG.base_url needs /v1 (codex appends /responses
// directly, unlike the Claude SDK which appends /v1/messages). So we append /v1
// to the gateway base URL here.
func buildCodexAgent() (acp.Agent, map[string]any) {
	agent := acp.CreateCodexAgent(acp.Agent{})
	if !gatewayConfigured() {
		return agent, nil
	}
	model := os.Getenv("CODEX_GATEWAY_MODEL")
	if model == "" {
		model = "deepseek-chat"
	}
	// Codex base_url needs /v1 (it appends /responses, not /v1/responses).
	baseURL := strings.TrimSuffix(os.Getenv(gatewayBaseURLEnv), "/") + "/v1"
	key := os.Getenv(gatewayKeyEnv)
	codexConfig := fmt.Sprintf(`{
  "model_provider": "unified-router",
  "model": %q,
  "model_providers": {
    "unified-router": {
      "name": "Unified Router",
      "base_url": %q,
      "env_key": "OPENAI_API_KEY",
      "wire_api": "responses"
    }
  }
}`, model, baseURL)
	agent.Env = map[string]string{
		"OPENAI_API_KEY": key,
		"CODEX_CONFIG":   codexConfig,
	}
	return agent, nil
}

// runAgentCheck spawns the real agent, runs a minimal prompt, and verifies the
// sentinel token appears in the output. Returns (status, detail).
func runAgentCheck(build func() (acp.Agent, map[string]any), label string) (status string, detail string) {
	var stderrMu sync.Mutex
	var stderrTail string
	defer func() {
		stderrMu.Lock()
		defer stderrMu.Unlock()
		if stderrTail != "" {
			detail += "; stderr tail: " + stderrTail
		}
		detail = redactDiagnostic(detail)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()

	cwd, err := os.MkdirTemp("", "acp-compat-smoke-")
	if err != nil {
		return "INFRA_ERROR", fmt.Sprintf("create isolated smoke directory: %v", err)
	}
	defer os.RemoveAll(cwd)

	start := time.Now()
	runtime := acp.NewRuntime(acp.NewStdioConnectionFactory(acp.StdioFactoryOptions{
		OnProcessExit: func(_ error, tail string) { stderrMu.Lock(); stderrTail = redactDiagnostic(tail); stderrMu.Unlock() },
	}), acp.RuntimeOptions{})

	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if closeErr := runtime.Close(cleanup); closeErr != nil {
			if status == "PASS" {
				status = classifyCheckResult(closeErr, "")
			}
			detail += "; cleanup: " + closeErr.Error()
		}
	}()
	agent, meta := build()
	if !strings.HasSuffix(label, "-native") {
		if agent.Env == nil {
			agent.Env = map[string]string{}
		}
		// Wrapper smoke uses explicit environment credentials, never a user's
		// per-project or home configuration/login. Native retains its CLI login.
		home := filepath.Join(cwd, "home")
		if err := os.MkdirAll(home, 0700); err != nil {
			return "INFRA_ERROR", err.Error()
		}
		agent.Env["HOME"] = home
		agent.Env["CODEX_HOME"] = filepath.Join(home, ".codex")
		agent.Env["CLAUDE_CONFIG_DIR"] = filepath.Join(home, ".claude")
		agent.Env["XDG_CONFIG_HOME"] = filepath.Join(home, ".config")
	}
	opts := acp.StartSessionOptions{Agent: agent, CWD: cwd, Meta: meta}
	session, err := runtime.StartSession(ctx, opts)
	if err != nil {
		return classifyCheckResult(err, ""), fmt.Sprintf("StartSession error: %v", err)
	}

	prompt := fmt.Sprintf("Reply with exactly and only: %s", sentinelToken)
	completion, err := session.Run(ctx, prompt)
	elapsed := time.Since(start).Truncate(100 * time.Millisecond)
	if err != nil {
		return classifyCheckResult(err, completion.OutputText), fmt.Sprintf("Run error after %s: %v", elapsed, err)
	}
	if status := classifyCheckResult(nil, completion.OutputText); status != "PASS" {
		return status, fmt.Sprintf("output=%q (missing %s) after %s", completion.OutputText, sentinelToken, elapsed)
	}
	return "PASS", fmt.Sprintf("output=%q, %s", completion.OutputText, elapsed)
}
