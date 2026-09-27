package acpruntime

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// Native-transport tests against the local magpie gateway. Opt-in: set
// MAGPIE_BASE_URL (e.g. http://127.0.0.1:3425/v1) and MAGPIE_API_KEY.
//
// Discovered model availability on this gateway (2026-09-25):
//   - anthropic wire (/v1/messages, streaming): cursor/auto works
//     (cursor/claude-* is region-blocked upstream; copilot/* rejected)
//   - chat wire (/v1/chat/completions): cursor/auto works
//   - responses wire: upstream hangs — codex uses wire_api=chat here.

func magpieBaseURL(t *testing.T) (base, key string) {
	t.Helper()
	base = os.Getenv("MAGPIE_BASE_URL")
	if base == "" {
		t.Skip("set MAGPIE_BASE_URL to run the magpie gateway tests")
	}
	key = os.Getenv("MAGPIE_API_KEY")
	if key == "" {
		key = "magpie"
	}
	return base, key
}

// anthropicBase strips the /v1 suffix: the claude CLI appends /v1/messages
// to ANTHROPIC_BASE_URL itself.
func anthropicBase(openaiBase string) string {
	return strings.TrimSuffix(strings.TrimSuffix(openaiBase, "/"), "/v1")
}

func TestClaudeNativeGateway(t *testing.T) {
	base, key := magpieBaseURL(t)
	// Discovered on this gateway (upstreams fluctuate; rescans help):
	// copilot/claude-sonnet-4.6 is stable on the anthropic streaming wire
	// and accepts reasoning effort (haiku 4.5 rejects effort=max).
	model := os.Getenv("MAGPIE_CLAUDE_MODEL")
	if model == "" {
		model = "copilot/claude-sonnet-4.6"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	agent := CreateClaudeCodeNativeAgent(Agent{
		Env: map[string]string{
			"ANTHROPIC_BASE_URL":          anthropicBase(base),
			"ANTHROPIC_API_KEY":           key,
			"ANTHROPIC_MODEL":             model,
			"ANTHROPIC_SMALL_FAST_MODEL":  model,
			"ANTHROPIC_DEFAULT_HAIKU_MODEL":  model,
			"ANTHROPIC_DEFAULT_SONNET_MODEL": model,
			"ANTHROPIC_DEFAULT_OPUS_MODEL":   model,
		},
	})
	runtime := NewRuntime(nil, RuntimeOptions{})
	session, err := runtime.StartSession(ctx, StartSessionOptions{
		Agent: agent,
		CWD:   "/tmp",
		Meta:  map[string]any{"model": model}, // -> --model flag via the native adapter
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer func() { _ = session.Close(context.Background()) }()

	completion, err := session.Run(ctx, "Reply with exactly and only: MAGPIE_CLAUDE_OK")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	t.Logf("output=%q stopReason=%s usage=%+v", completion.OutputText, completion.StopReason, completion.Usage)
	if !strings.Contains(completion.OutputText, "MAGPIE_CLAUDE_OK") {
		t.Fatalf("OutputText = %q, want MAGPIE_CLAUDE_OK", completion.OutputText)
	}
}

func TestCodexNativeGateway(t *testing.T) {
	base, key := magpieBaseURL(t)
	// codex 0.153.4 requires the responses wire; codex/gpt-5.5 verified
	// stable on it (3/3 probes).
	model := os.Getenv("MAGPIE_CODEX_MODEL")
	if model == "" {
		model = "codex/gpt-5.5"
	}
	// The gateway's upstreams are shared infrastructure that intermittently
	// 502s; retry the whole turn up to 3 times before failing.
	var completion TurnCompletion
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		completion, lastErr = runCodexGatewayTurn(t, base, key, model)
		if lastErr == nil {
			break
		}
		t.Logf("attempt %d failed: %v", attempt, lastErr)
		time.Sleep(2 * time.Second)
	}
	if lastErr != nil {
		// A 502 from the gateway is its upstream failing to reach the model
		// provider — environmental, not a transport regression (the transport
		// contract is covered by the fake-engine E2E). Report and skip.
		if strings.Contains(lastErr.Error(), "502") {
			t.Skipf("gateway upstream unhealthy (502 from %s): %v", base, lastErr)
		}
		t.Fatalf("Run after 3 attempts: %v", lastErr)
	}

	if !strings.Contains(completion.OutputText, "MAGPIE_CODEX_OK") {
		t.Fatalf("OutputText = %q, want MAGPIE_CODEX_OK", completion.OutputText)
	}
}

// runCodexGatewayTurn drives one codex-native turn against the gateway.
func runCodexGatewayTurn(t *testing.T, base, key, model string) (TurnCompletion, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	providerConfig, _ := json.Marshal(map[string]any{
		"model": model,
		"model_provider": "magpie",
		"model_providers": map[string]any{
			"magpie": map[string]any{
				"name":     "Magpie",
				"base_url": base, // codex appends /responses itself (wire_api=responses; codex 0.153.4 removed "chat")
				"env_key":  "OPENAI_API_KEY",
				"wire_api": "responses",
			},
		},
	})
	agent := CreateCodexNativeAgent(Agent{
		Env: map[string]string{
			"OPENAI_API_KEY": key,
			"CODEX_CONFIG":   string(providerConfig),
		},
	})
	if home := os.Getenv("CODEX_NATIVE_TEST_HOME"); home != "" {
		agent.Env["CODEX_HOME"] = home
	}
	runtime := NewRuntime(nil, RuntimeOptions{})
	session, err := runtime.StartSession(ctx, StartSessionOptions{Agent: agent, CWD: "/tmp"})
	if err != nil {
		return TurnCompletion{}, err
	}
	defer func() { _ = session.Close(context.Background()) }()
	return session.Run(ctx, "Reply with exactly and only: MAGPIE_CODEX_OK")
}
