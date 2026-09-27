package acpruntime

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

// compareVersions anchors the semver-ish ordering used for version floors.
func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"0.153.4", "0.153.4", 0},
		{"0.153.4", "0.153.5", -1},
		{"0.154.0", "0.153.9", 1},
		{"1.0", "0.99.99", 1},
		{"0.153", "0.153.1", -1}, // shorter prefix sorts lower
	}
	for _, tc := range cases {
		if got := compareVersions(tc.a, tc.b); got != tc.want {
			t.Fatalf("compareVersions(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

// The bridge ships probe results in initialize _meta; below-floor engines
// must surface a visible warning through Session.Diagnostics().
func TestNativeBelowFloorWarningSurfacesInDiagnostics(t *testing.T) {
	providerReader, runtimeWriter := io.Pipe()
	runtimeReader, providerWriter := io.Pipe()
	runtimePeer := NewPeer(runtimeReader, runtimeWriter, PeerOptions{})
	providerPeer := NewPeer(providerReader, providerWriter, PeerOptions{})
	providerPeer.RegisterRequest("session/prompt", func(context.Context, json.RawMessage) (any, error) {
		return PromptResponse{StopReason: "end_turn"}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = runtimePeer.Start(ctx) }()
	go func() { _ = providerPeer.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		runtimePeer.Close()
		providerPeer.Close()
		_ = providerReader.Close()
		_ = runtimeWriter.Close()
		_ = runtimeReader.Close()
		_ = providerWriter.Close()
	})

	bootstrap := sessionBootstrap{
		Agent:     Agent{Type: CodexNativeRegistryID},
		CWD:       ".",
		Connection: NewConnection(runtimePeer, Client{}),
		Dispose:   func(context.Context) error { return nil },
		InitializeResponse: InitializeResponse{
			Meta: map[string]any{
				"x-acp-runtime-native": map[string]any{
					"engine":             "codex",
					"version":            "0.140.0",
					"verifiedFloor":      "0.153.4",
					"belowVerifiedFloor": true,
				},
			},
		},
		SessionResponse: NewSessionResponse{SessionID: "session-1"},
		Profile:         defaultAgentProfile(),
		QueuePolicy:     resolveQueuePolicy(QueuePolicyInput{}),
	}
	driver := newACPSessionDriver(bootstrap)
	diag := driver.Diagnostics()
	found := false
	for _, warning := range diag.Warnings {
		if strings.Contains(warning, "below the verified floor") {
			found = true
		}
	}
	if !found {
		t.Fatalf("warnings = %v, want below-floor warning", diag.Warnings)
	}
	info, ok := diag.Raw["nativeEngine"].(map[string]any)
	if !ok || info["engine"] != "codex" {
		t.Fatalf("diagnostics raw missing nativeEngine info: %+v", diag.Raw)
	}
}

// TestProbeNativeEngineVersion verifies token extraction + caching semantics
// against a command that is guaranteed present and supports --version (git).
func TestProbeNativeEngineVersion(t *testing.T) {
	version, err := ProbeNativeEngineVersion(context.Background(), "git")
	if err != nil {
		t.Skipf("git not usable in this environment: %v", err)
	}
	if version == "" {
		t.Fatal("empty version")
	}
	// Cached second call must succeed without re-running the command.
	again, err := ProbeNativeEngineVersion(context.Background(), "git")
	if err != nil || again != version {
		t.Fatalf("cached probe = %q, %v; want %q, nil", again, err, version)
	}
}
