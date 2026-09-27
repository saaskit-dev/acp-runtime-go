package acpruntime

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// NativeEngineVerifiedVersions records the oldest CLI versions the native
// adapters have been VERIFIED against (live round-trips in this repository).
// Older engines may still work — the runtime does not refuse them — but the
// bridge reports belowVerifiedFloor in the initialize _meta and the driver
// surfaces a warning through Session.Diagnostics() so hosts can see the gap.
var NativeEngineVerifiedVersions = map[string]string{
	"codex":  "0.153.4",
	"claude": "2.1.215",
}

var (
	nativeVersionOnce   sync.Map // command -> string ("" marks a failed probe)
	nativeVersionTokens = regexp.MustCompile(`\d+(?:\.\d+)+`)
)

// ProbeNativeEngineVersion runs "<command> --version" and extracts the first
// version-like token (e.g. "codex-cli 0.153.4" -> "0.153.4"). The result is
// cached per command for the process lifetime.
func ProbeNativeEngineVersion(ctx context.Context, command string) (string, error) {
	if cached, ok := nativeVersionOnce.Load(command); ok {
		v := cached.(string)
		if v == "" {
			return "", fmt.Errorf("version probe previously failed for %s", command)
		}
		return v, nil
	}
	out, err := exec.CommandContext(ctx, command, "--version").Output()
	if err != nil {
		nativeVersionOnce.Store(command, "")
		return "", fmt.Errorf("%s --version: %w", command, err)
	}
	token := nativeVersionTokens.FindString(string(out))
	nativeVersionOnce.Store(command, token)
	if token == "" {
		return "", fmt.Errorf("no version token in %s --version output: %q", command, strings.TrimSpace(string(out)))
	}
	return token, nil
}

// compareVersions compares dotted numeric versions; negative when a < b.
func compareVersions(a, b string) int {
	parts := func(v string) []int {
		var nums []int
		for _, field := range strings.Split(v, ".") {
			n, err := strconv.Atoi(field)
			if err != nil {
				break
			}
			nums = append(nums, n)
		}
		return nums
	}
	aParts, bParts := parts(a), parts(b)
	for i := 0; i < len(aParts) || i < len(bParts); i++ {
		var av, bv int
		if i < len(aParts) {
			av = aParts[i]
		}
		if i < len(bParts) {
			bv = bParts[i]
		}
		if av != bv {
			if av < bv {
				return -1
			}
			return 1
		}
	}
	return 0
}

// Native transport registry IDs. An Agent created with one of these Types
// bypasses the external ACP wrapper process and drives the CLI's own headless
// protocol through an in-process bridge (native_bridge.go). The public
// surface — Runtime, Session, read models, authority callbacks — is identical
// to the ACP path; only the launch transport differs.
const (
	ClaudeCodeNativeRegistryID = "claude-native"
	CodexNativeRegistryID      = "codex-native"
)

// CreateCodexNativeAgent builds an Agent that drives the Codex CLI directly
// over its native app-server JSON-RPC protocol (spawn: codex app-server
// --listen stdio://). Configuration still flows through CODEX_CONFIG env,
// exactly like the ACP path. Overrides may replace Command/Args (tests do).
func CreateCodexNativeAgent(overrides Agent) Agent {
	return mergeAgent(Agent{
		Type:    CodexNativeRegistryID,
		Command: "codex",
		Args:    []string{"app-server", "--listen", "stdio://"},
	}, overrides)
}

// CreateClaudeCodeNativeAgent builds an Agent that drives the Claude Code CLI
// directly over its native headless stream-json protocol (spawn: claude -p
// --input-format stream-json --output-format stream-json --verbose). System
// prompt, model, tool permissions and MCP servers from the unified session
// options translate to spawn flags; the public API is identical to the ACP
// path.
func CreateClaudeCodeNativeAgent(overrides Agent) Agent {
	return mergeAgent(Agent{Type: ClaudeCodeNativeRegistryID, Command: "claude"}, overrides)
}

// withNativeTransport routes ConnectionFactory selection by Agent.Type.
// Unknown/native-absent types delegate to the base factory unchanged, so the
// switch is invisible to existing hosts.
func withNativeTransport(base ConnectionFactory) ConnectionFactory {
	return func(ctx context.Context, input ConnectionFactoryInput) (ConnectionHandle, error) {
		switch input.Agent.Type {
		case CodexNativeRegistryID:
			return newNativeBridgeConnection(ctx, input, func() nativeEngine { return &codexNativeEngine{} })
		case ClaudeCodeNativeRegistryID:
			return newNativeBridgeConnection(ctx, input, func() nativeEngine { return &claudeNativeEngine{} })
		}
		return base(ctx, input)
	}
}
