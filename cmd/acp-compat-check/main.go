// Command acp-compat-check verifies that the latest published ACP wrapper
// packages still work with this runtime. It is designed to run both locally
// (go run ./cmd/acp-compat-check) and in CI (scheduled workflow).
//
// To avoid burning API quota on unchanged versions, it caches the last
// successfully-tested version of each wrapper in a small JSON file
// (.compat-versions.json, or the path in COMPAT_CACHE). When the npm latest
// version matches the cached version, the expensive spawn+prompt smoke test is
// skipped (reported as CACHED). The test only runs when a version is new or the
// cached entry is absent.
//
// For each wrapper:
//  1. Query npm for the current latest version.
//  2. If the version matches the cached "last tested OK" version → CACHED (skip).
//  3. Else if the API key env var is present, spawn the real agent and run a
//     minimal prompt; on PASS, update the cache with the new version.
//  4. Report PASS / FAIL / SKIPPED / INFRA_ERROR / CACHED.
//
// Exit codes: 0 = all PASS/CACHED; 1 = compatibility FAIL; 2 = incomplete check.
// Invoke the compiled binary in CI: go run maps nonzero program exits to 1.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
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
	name      string // human label
	pkg       string // npm package name for version query + cache key
	buildFunc func() (acp.Agent, map[string]any) // agent + optional session/new _meta
	apiKeyEnv string                            // env var that must be present to run the real test
	// localVersion, when set, sources the version from the LOCAL CLI instead
	// of npm (native transports drive the user's own binary).
	localVersion func() (string, error)
	// localAuth marks engines that authenticate through their own CLI login
	// (no provider API key env is required).
	localAuth   bool
	localBinary string
}

func main() {
	checks := []agentCheck{
		{
			name:      "claude-agent-acp",
			pkg:       "@agentclientprotocol/claude-agent-acp",
			buildFunc: buildClaudeAgent,
			apiKeyEnv: "ANTHROPIC_API_KEY",
		},
		{
			name:      "codex-acp",
			pkg:       "@agentclientprotocol/codex-acp",
			buildFunc: buildCodexAgent,
			apiKeyEnv: "OPENAI_API_KEY", // CODEX_API_KEY also accepted; checked in apiKeyPresent
		},
		{
			name:      "codex-native",
			pkg:       "native:codex",
			buildFunc: buildNativeCodexAgent,
			localVersion: func() (string, error) {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				return acp.ProbeNativeEngineVersion(ctx, "codex")
			},
			localAuth:   true,
			localBinary: "codex",
		},
		{
			name:      "claude-native",
			pkg:       "native:claude",
			buildFunc: buildNativeClaudeAgent,
			localVersion: func() (string, error) {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				return acp.ProbeNativeEngineVersion(ctx, "claude")
			},
			localAuth:   true,
			localBinary: "claude",
		},
	}

	cachePath := cacheFilePath()
	cache, _ := loadCache(cachePath) // missing/invalid cache is fine → all "new"
	cacheDirty := false

	fmt.Printf("acp-compat-check — %s\n", time.Now().UTC().Format("2006-01-02 15:04:05 UTC"))
	fmt.Printf("cache: %s\n", cachePath)
	if gatewayConfigured() {
		fmt.Printf("gateway: %s (via %s + %s)\n", os.Getenv(gatewayBaseURLEnv), gatewayBaseURLEnv, gatewayKeyEnv)
	}
	fmt.Println()

	hasFailure := false
	hasIncomplete := false

	for _, c := range checks {
		// A missing native CLI is an unmet prerequisite, even with a cache entry.
		if c.localAuth && !c.canRun() {
			fmt.Printf("%s: spawn+prompt: SKIPPED (%s CLI unavailable on PATH)\n\n", c.name, c.localBinary)
			hasIncomplete = true
			continue
		}
		version, vErr := c.version()
		if vErr != nil {
			fmt.Printf("%s: INFRA_ERROR (could not query engine version: %v)\n\n", c.name, vErr)
			hasIncomplete = true
			continue
		} else {
			fmt.Printf("%s: latest=%s\n", c.name, version)
		}

		// Fast path: version unchanged since last successful test → skip the
		// expensive spawn+prompt. This is the key optimization: day-to-day,
		// when nothing changed, we do a single `npm view` per agent and stop.
		if version != "unknown" && cache[c.pkg] == version {
			fmt.Printf("  spawn+prompt: CACHED (already tested v%s)\n\n", version)
			continue
		}

		if cache[c.pkg] != "" {
			fmt.Printf("  (cached was v%s, version changed)\n", cache[c.pkg])
		}

		if !c.canRun() {
			fmt.Printf("  spawn+prompt: SKIPPED (no %s and no gateway; version uncached)\n\n", c.apiKeyEnv)
			hasIncomplete = true
			continue
		}

		status, detail := runAgentCheck(c.buildFunc, c.name)
		switch status {
		case "PASS":
			fmt.Printf("  spawn+prompt: PASS (%s)\n\n", detail)
			if version != "unknown" {
				cache[c.pkg] = version
				cacheDirty = true
			}
		case "INFRA_ERROR":
			fmt.Printf("  spawn+prompt: INFRA_ERROR (%s)\n\n", detail)
			hasIncomplete = true
		case "FAIL":
			fmt.Printf("  spawn+prompt: FAIL (%s)\n\n", detail)
			hasFailure = true
			// Do NOT update cache on failure: next run will retry the same
			// version, which is what we want (transient failures self-heal).
		}
	}

	// Persist updated cache so future runs skip unchanged versions.
	if cacheDirty {
		if err := saveCache(cachePath, cache); err != nil {
			fmt.Printf("⚠ could not write cache: %v\n", err)
		}
	}

	if resultExitCode(hasFailure, hasIncomplete) == 1 {
		fmt.Println("Result: FAIL — at least one agent did not produce the expected output.")
		os.Exit(1)
	}
	if hasIncomplete {
		fmt.Println("Result: INCOMPLETE — prerequisites or infrastructure prevented compatibility checks.")
		os.Exit(2)
	}
	fmt.Println("Result: OK — all agents PASS or CACHED.")
	os.Exit(0)
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

func loadCache(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]string{}, err
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return map[string]string{}, err
	}
	return m, nil
}

func saveCache(path string, cache map[string]string) error {
	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
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
	return strings.TrimSpace(string(out)), nil
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
func runAgentCheck(build func() (acp.Agent, map[string]any), label string) (string, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()

	cwd, err := os.Getwd()
	if err != nil {
		return "INFRA_ERROR", fmt.Sprintf("os.Getwd: %v", err)
	}

	start := time.Now()
	runtime := acp.NewRuntime(acp.NewStdioConnectionFactory(acp.StdioFactoryOptions{
		Stderr: "ignore", // keep CI logs clean; errors surface via empty output
	}), acp.RuntimeOptions{})

	agent, meta := build()
	opts := acp.StartSessionOptions{Agent: agent, CWD: cwd, Meta: meta}
	session, err := runtime.StartSession(ctx, opts)
	if err != nil {
		return classifyCheckResult(err, ""), fmt.Sprintf("StartSession error: %v", err)
	}
	defer session.Close(context.Background())

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
