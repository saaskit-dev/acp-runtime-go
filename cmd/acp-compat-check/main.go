// Command acp-compat-check verifies that the latest published ACP wrapper
// packages still work with this runtime. It is designed to run both locally
// (go run ./cmd/acp-compat-check) and in CI (scheduled workflow).
//
// For each wrapper it:
//  1. Queries npm for the current latest version.
//  2. If the corresponding API key env var is present, spawns the real agent
//     and runs a minimal prompt, asserting the runtime can drive a full turn.
//  3. Reports PASS / FAIL / SKIPPED.
//
// Exit codes: 0 = all tested agents PASS; 1 = at least one FAIL; 2 = all agents
// SKIPPED (no API keys configured, so nothing was actually tested).
package main

import (
	"context"
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
	pkg       string // npm package name for version query
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
			name: "codex-acp",
			pkg:  "@agentclientprotocol/codex-acp",
			buildFunc: func() acp.Agent {
				return acp.CreateCodexAgent(acp.Agent{})
			},
			apiKeyEnv: "OPENAI_API_KEY", // CODEX_API_KEY also accepted by codex; checked below
		},
	}

	fmt.Printf("acp-compat-check — %s\n\n", time.Now().UTC().Format("2006-01-02 15:04:05 UTC"))

	hasFailure := false
	hasPass := false
	hasSkipped := false

	for _, c := range checks {
		version, vErr := npmLatestVersion(c.pkg)
		if vErr != nil {
			fmt.Printf("%s: ⚠ could not query npm version (%v)\n", c.name, vErr)
			version = "unknown"
		} else {
			fmt.Printf("%s: latest=%s\n", c.name, version)
		}

		if !apiKeyPresent(c.apiKeyEnv) {
			fmt.Printf("  spawn+prompt: SKIPPED (no %s)\n\n", c.apiKeyEnv)
			hasSkipped = true
			continue
		}

		status, detail := runAgentCheck(c.buildFunc, c.name)
		switch status {
		case "PASS":
			fmt.Printf("  spawn+prompt: PASS (%s)\n\n", detail)
			hasPass = true
		case "FAIL":
			fmt.Printf("  spawn+prompt: FAIL (%s)\n\n", detail)
			hasFailure = true
		}
	}

	// Exit code logic: failure dominates; if no failures and no passes, it was
	// all skipped (no API keys) -> exit 2 so CI can distinguish "untested".
	switch {
	case hasFailure:
		fmt.Println("Result: FAIL — at least one agent did not produce the expected output.")
		os.Exit(1)
	case hasSkipped && !hasPass:
		fmt.Println("Result: SKIPPED — no API keys configured, nothing was tested.")
		os.Exit(2)
	default:
		fmt.Println("Result: PASS — all tested agents are compatible.")
		os.Exit(0)
	}
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
