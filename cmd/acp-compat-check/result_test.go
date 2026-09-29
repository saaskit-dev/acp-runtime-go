package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestClassifyCheckResult(t *testing.T) {
	// The gateway diagnostic from issue #9 is ordinary OutputText, not a Go error.
	balance := "Warning: Model metadata for deepseek-chat not found.\n" +
		`unexpected status 402 Payment Required: {"type":"error","code":"upstream_error","message":"Insufficient Balance"}`
	tests := []struct {
		name   string
		err    error
		output string
		want   string
	}{
		{"sentinel", nil, sentinelToken, "PASS"},
		{"issue9 gateway output", nil, balance, "INFRA_ERROR"},
		{"gateway run error", errors.New(balance), "", "INFRA_ERROR"},
		{"wrapped missing executable", fmt.Errorf("spawn: %w", exec.ErrNotFound), "", "INFRA_ERROR"},
		{"native bootstrap error", errors.New(`rpc error -32000: native.codex.spawn: codex binary not found on PATH`), "", "INFRA_ERROR"},
		{"credentials", errors.New("authentication_error: invalid key"), "", "INFRA_ERROR"},
		{"native login", nil, "Not logged in. Please run login.", "INFRA_ERROR"},
		{"unauthorized output", nil, "unexpected status 401 Unauthorized", "INFRA_ERROR"},
		{"forbidden", errors.New("HTTP status 403 Forbidden"), "", "INFRA_ERROR"},
		{"rate limit", nil, "status code: 429", "INFRA_ERROR"},
		{"provider unavailable", nil, "HTTP 503 Service Unavailable", "INFRA_ERROR"},
		{"timeout", fmt.Errorf("run: %w", context.DeadlineExceeded), "", "INFRA_ERROR"},
		{"dns", &net.DNSError{Err: "no such host", Name: "gateway.invalid"}, "", "INFRA_ERROR"},
		{"filesystem", fmt.Errorf("spawn: %w", os.ErrPermission), "", "INFRA_ERROR"},
		{"protocol method", errors.New("rpc error -32601: Method not found"), "", "FAIL"},
		{"protocol params", errors.New("rpc error -32602: Invalid params"), "", "FAIL"},
		{"unknown rpc", errors.New("rpc error -32000: unexpected response"), "", "FAIL"},
		{"malformed protocol", errors.New("invalid character in JSON response"), "", "FAIL"},
		{"missing sentinel", nil, "hello", "FAIL"},
		{"empty output", nil, "", "FAIL"},
		{"bare status number", nil, "402", "FAIL"},
		{"recovered request", nil, "HTTP 503 retrying\n" + sentinelToken, "PASS"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyCheckResult(tt.err, tt.output); got != tt.want {
				t.Fatalf("got %s, want %s", got, tt.want)
			}
		})
	}
}

func TestNativeCanRunRequiresCLI(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	for _, binary := range []string{"codex", "claude"} {
		check := agentCheck{localAuth: true, localBinary: binary}
		if check.canRun() {
			t.Fatalf("%s must be skipped when absent", binary)
		}
		if err := os.WriteFile(filepath.Join(dir, binary), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		if !check.canRun() {
			t.Fatalf("%s on PATH must be runnable", binary)
		}
	}
}

func TestResultExitCode(t *testing.T) {
	for _, tt := range []struct {
		fail, incomplete bool
		want             int
	}{
		{false, false, 0}, // PASS/CACHED: may close a stale issue.
		{false, true, 2},  // INFRA_ERROR/SKIPPED: neither open nor close issues.
		{true, false, 1},  // Genuine compatibility failure: open/update issue.
		{true, true, 1},   // Infrastructure must not hide a simultaneous regression.
	} {
		if got := resultExitCode(tt.fail, tt.incomplete); got != tt.want {
			t.Fatalf("fail=%v incomplete=%v: got %d, want %d", tt.fail, tt.incomplete, got, tt.want)
		}
	}
}

func TestMainSkipsMissingNativeCLI(t *testing.T) {
	if os.Getenv("COMPAT_TEST_HELPER") == "1" {
		main()
		return
	}
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture requires Unix")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "npm"), []byte("#!/bin/sh\necho 1.0.0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cachePath := filepath.Join(dir, "cache.json")
	cache := map[string]string{
		"@agentclientprotocol/claude-agent-acp": "1.0.0",
		"@agentclientprotocol/codex-acp": "1.0.0",
		"native:codex": "1.0.0",
		"native:claude": "1.0.0",
	}
	if err := saveCache(cachePath, cache); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("COMPAT_CACHE", cachePath)
	t.Setenv("COMPAT_TEST_HELPER", "1")
	cmd := exec.Command(os.Args[0], "-test.run=^TestMainSkipsMissingNativeCLI$")
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 2 {
		t.Fatalf("want incomplete exit 2, got %v; output=%s", err, out)
	}
	for _, binary := range []string{"codex", "claude"} {
		if !strings.Contains(string(out), binary+"-native: spawn+prompt: SKIPPED ("+binary+" CLI unavailable on PATH)") {
			t.Fatalf("missing skip diagnostic for %s: %s", binary, out)
		}
	}
	if strings.Contains(string(out), "FAIL") {
		t.Fatalf("missing native CLI must not report regression: %s", out)
	}
	after, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("incomplete checks must not update cached versions")
	}
}
