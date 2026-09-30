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
	"time"
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

func TestMainWrapperScopeSkipsMissingNativeCLI(t *testing.T) {
	if os.Getenv("COMPAT_TEST_HELPER") == "1" {
		os.Exit(runMain(nil))
	}
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture requires Unix")
	}
	dir := t.TempDir()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(git, filepath.Join(dir, "git")); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ncase \"$3\" in version) echo 1.0.0;; *) echo sha512-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA==;; esac\n"
	if err = os.WriteFile(filepath.Join(dir, "npm"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("COMPAT_SUITES", "wrapper")
	t.Setenv("COMPAT_REQUIRED", "wrapper")
	t.Setenv("COMPAT_TEST_HELPER", "1")
	t.Setenv("COMPAT_SUMMARY", filepath.Join(dir, "summary.json"))
	t.Setenv("COMPAT_CACHE", filepath.Join(dir, "cache.json"))
	base, err := runtimeIdentity()
	if err != nil {
		t.Fatal(err)
	}
	cache := newCache()
	now := time.Now().UTC()
	for _, check := range availableChecks() {
		if check.localAuth {
			continue
		}
		id := base
		id.Suite = "wrapper"
		id.Engine = check.name
		id.EngineVersion = "1.0.0"
		id.ArtifactDigest = "sha512-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=="
		cache.Entries[id.key()] = cacheEntry{Identity: id, Status: statusPass, VerifiedAt: now, ExpiresAt: now.Add(time.Hour), Source: "fixture"}
	}
	if err = saveCache(cacheFilePath(), cache); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(cacheFilePath())
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestMainWrapperScopeSkipsMissingNativeCLI$")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("wrapper scope must exit 0: %v: %s", err, out)
	}
	t.Logf("wrapper-scope exit 0:\n%s", out)
	for _, want := range []string{"wrapper/claude-agent-acp: CACHED", "wrapper/codex-acp: CACHED", "native/codex-native: SKIPPED (suite not selected)", "native/claude-native: SKIPPED (suite not selected)"} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("missing %q in %s", want, out)
		}
	}
	after, _ := os.ReadFile(cacheFilePath())
	if string(before) != string(after) {
		t.Fatal("cache hit changed evidence")
	}
	t.Setenv("COMPAT_SUITES", "wrapper,native")
	t.Setenv("COMPAT_REQUIRED", "wrapper,native")
	cmd = exec.Command(os.Args[0], "-test.run=^TestMainWrapperScopeSkipsMissingNativeCLI$")
	out, err = cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 2 {
		t.Fatalf("required missing native must exit 2: %v: %s", err, out)
	}
	t.Logf("required-native exit 2:\n%s", out)
}
