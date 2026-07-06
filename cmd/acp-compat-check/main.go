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
//  4. Report PASS / FAIL / SKIPPED / CACHED.
//
// Exit codes: 0 = no FAIL (all PASS, CACHED, or SKIPPED); 1 = at least one FAIL.
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

type agentCheck struct {
	name      string // human label
	pkg       string // npm package name for version query + cache key
	buildFunc func() acp.Agent
	apiKeyEnv string // env var that must be present to run the real test
}

func main() {
	checks := []agentCheck{
		{
			name:      "claude-agent-acp",
			pkg:       "@agentclientprotocol/claude-agent-acp",
			buildFunc: func() acp.Agent { return acp.CreateClaudeCodeAgent(acp.Agent{}) },
			apiKeyEnv: "ANTHROPIC_API_KEY",
		},
		{
			name:      "codex-acp",
			pkg:       "@agentclientprotocol/codex-acp",
			buildFunc: func() acp.Agent { return acp.CreateCodexAgent(acp.Agent{}) },
			apiKeyEnv: "OPENAI_API_KEY", // CODEX_API_KEY also accepted; checked in apiKeyPresent
		},
	}

	cachePath := cacheFilePath()
	cache, _ := loadCache(cachePath) // missing/invalid cache is fine → all "new"
	cacheDirty := false

	fmt.Printf("acp-compat-check — %s\n", time.Now().UTC().Format("2006-01-02 15:04:05 UTC"))
	fmt.Printf("cache: %s\n\n", cachePath)

	hasFailure := false

	for _, c := range checks {
		version, vErr := npmLatestVersion(c.pkg)
		if vErr != nil {
			fmt.Printf("%s: ⚠ could not query npm version (%v)\n", c.name, vErr)
			// Can't determine version → fall through to test if key is present,
			// treating it as uncached so we don't silently skip on npm errors.
			version = "unknown"
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

		if !apiKeyPresent(c.apiKeyEnv) {
			fmt.Printf("  spawn+prompt: SKIPPED (no %s; version uncached)\n\n", c.apiKeyEnv)
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

	if hasFailure {
		fmt.Println("Result: FAIL — at least one agent did not produce the expected output.")
		os.Exit(1)
	}
	fmt.Println("Result: OK — no failures (all PASS, CACHED, or SKIPPED).")
	os.Exit(0)
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

// runAgentCheck spawns the real agent, runs a minimal prompt, and verifies the
// sentinel token appears in the output. Returns (status, detail).
func runAgentCheck(build func() acp.Agent, label string) (string, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()

	cwd, err := os.Getwd()
	if err != nil {
		return "FAIL", fmt.Sprintf("os.Getwd: %v", err)
	}

	start := time.Now()
	runtime := acp.NewRuntime(acp.NewStdioConnectionFactory(acp.StdioFactoryOptions{
		Stderr: "ignore", // keep CI logs clean; errors surface via empty output
	}), acp.RuntimeOptions{})

	agent := build()
	session, err := runtime.StartSession(ctx, acp.StartSessionOptions{Agent: agent, CWD: cwd})
	if err != nil {
		return "FAIL", fmt.Sprintf("StartSession error: %v", err)
	}
	defer session.Close(context.Background())

	prompt := fmt.Sprintf("Reply with exactly and only: %s", sentinelToken)
	completion, err := session.Run(ctx, prompt)
	elapsed := time.Since(start).Truncate(100 * time.Millisecond)
	if err != nil {
		return "FAIL", fmt.Sprintf("Run error after %s: %v", elapsed, err)
	}
	if !strings.Contains(completion.OutputText, sentinelToken) {
		return "FAIL", fmt.Sprintf("output=%q (missing %s) after %s", completion.OutputText, sentinelToken, elapsed)
	}
	return "PASS", fmt.Sprintf("output=%q, %s", completion.OutputText, elapsed)
}
