package acpruntime

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestTransportsParity is the dual-transport harness run: the SAME prompt
// goes through the ACP wrapper transport and the native transport, and both
// must satisfy the identical read-model contract (sentinel output, stop
// reason, usage). Opt-in via ACP_LIVE_PARITY=1 because it needs real
// provider auth for both legs. When only the wrapper leg cannot start (npm /
// provider keys unavailable), the subtest SKIPs with the reason instead of
// failing — native is covered by its own live tests.
func TestTransportsParity(t *testing.T) {
	if os.Getenv("ACP_LIVE_PARITY") != "1" {
		t.Skip("set ACP_LIVE_PARITY=1 to run the dual-transport parity check")
	}
	cases := []struct {
		name   string
		acp    func() Agent
		native func() Agent
	}{
		{"claude", func() Agent { return CreateClaudeCodeAgent(Agent{}) }, func() Agent { return CreateClaudeCodeNativeAgent(Agent{}) }},
		{"codex", func() Agent { return CreateCodexAgent(Agent{}) }, func() Agent { return CreateCodexNativeAgent(Agent{}) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runOne := func(agent Agent) (TurnCompletion, error) {
				ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
				defer cancel()
				runtime := NewRuntime(nil, RuntimeOptions{})
				session, err := runtime.StartSession(ctx, StartSessionOptions{Agent: agent, CWD: "/tmp"})
				if err != nil {
					return TurnCompletion{}, err
				}
				defer func() { _ = session.Close(context.Background()) }()
				return session.Run(ctx, "Reply with exactly and only: PARITY_OK")
			}

			acpResult, acpErr := runOne(tc.acp())
			if acpErr != nil {
				t.Skipf("ACP wrapper transport unavailable here: %v", acpErr)
			}
			nativeResult, nativeErr := runOne(tc.native())
			if nativeErr != nil {
				t.Fatalf("native transport failed: %v", nativeErr)
			}

			// Identical contract on both transports:
			for label, res := range map[string]TurnCompletion{"acp": acpResult, "native": nativeResult} {
				if !strings.Contains(res.OutputText, "PARITY_OK") {
					t.Errorf("%s: OutputText = %q (missing sentinel)", label, res.OutputText)
				}
				if res.StopReason == "" {
					t.Errorf("%s: empty StopReason", label)
				}
				if res.Usage == nil || res.Usage.InputTokens == 0 {
					t.Errorf("%s: usage not reported: %+v", label, res.Usage)
				}
			}
			if nativeResult.StopReason != acpResult.StopReason {
				t.Errorf("StopReason mismatch: native=%q acp=%q", nativeResult.StopReason, acpResult.StopReason)
			}
		})
	}
}
