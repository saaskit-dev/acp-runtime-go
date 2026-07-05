package acpruntime

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestResolveAgentCommand_PassesThroughWhenOnPath covers the common case where
// the parent process already has the agent command on its PATH (e.g. ConnectMe
// launched from a login shell). resolveAgentCommand must be a no-op there: it
// must not rewrite Env or invent a PATH.
func TestResolveAgentCommand_PassesThroughWhenOnPath(t *testing.T) {
	// Pick a binary that is guaranteed to exist on the minimal macOS/Linux PATH.
	bin := "sh"
	if runtime.GOOS == "windows" {
		bin = "cmd"
	}
	if _, err := exec.LookPath(bin); err != nil {
		t.Skipf("skipping: %q not on PATH: %v", bin, err)
	}

	agent := Agent{Command: bin}
	if err := resolveAgentCommand(&agent); err != nil {
		t.Fatalf("resolveAgentCommand error = %v", err)
	}
	if agent.Env != nil {
		t.Fatalf("Env should be untouched when command is already on PATH; got %v", agent.Env)
	}
}

// TestResolveAgentCommand_RecoversFromMinimalGUIPath simulates the ConnectMe
// failure: a GUI app inherits macOS's minimal PATH (/usr/bin:/bin:/usr/sbin:/sbin)
// which does not contain `npm`, even though npm is installed under a node
// version manager or Homebrew. resolveAgentCommand must locate the binary and
// inject the resolved directory into the agent's PATH env.
func TestResolveAgentCommand_RecoversFromMinimalGUIPath(t *testing.T) {
	// Find a real npm-like binary to relocate: reuse `go`'s own dir layout by
	// building a fake "npm" in a temp dir, then teaching resolveAgentCommand
	// about that dir via the candidate override.
	tmp := t.TempDir()
	fakeBin := filepath.Join(tmp, "npm")
	if err := os.WriteFile(fakeBin, []byte("#!/bin/sh\necho fake npm\n"), 0o755); err != nil {
		t.Fatalf("WriteFile error = %v", err)
	}

	// Force the parent PATH to the macOS GUI minimum so exec.LookPath fails.
	t.Setenv("PATH", "/usr/bin:/bin:/usr/sbin:/sbin")
	// Sanity: with the minimal PATH, exec.LookPath must fail before we recover.
	if _, err := exec.LookPath("npm"); err == nil {
		t.Fatalf("precondition failed: npm unexpectedly found on minimal PATH")
	}

	// Register the temp dir as a candidate so the test does not depend on the
	// machine having a real node install.
	prev := extraCandidateDirs
	t.Cleanup(func() { extraCandidateDirs = prev })
	extraCandidateDirs = []string{tmp}

	agent := Agent{Command: "npm"}
	if err := resolveAgentCommand(&agent); err != nil {
		t.Fatalf("resolveAgentCommand error = %v", err)
	}
	// Command must be rewritten to the absolute path so exec.Command skips
	// LookPath (which would consult the parent PATH again and fail).
	wantPath := filepath.Join(tmp, "npm")
	if agent.Command != wantPath {
		t.Fatalf("agent.Command = %q, want absolute path %q", agent.Command, wantPath)
	}
	injectedPath := agent.Env["PATH"]
	if injectedPath == "" {
		t.Fatalf("Env[PATH] not set; agent.Env = %v", agent.Env)
	}
	if !strings.Contains(injectedPath, tmp) {
		t.Fatalf("injected PATH %q does not contain candidate dir %q", injectedPath, tmp)
	}
	// The injected PATH must keep the original PATH appended so other tools
	// the agent shells out to remain reachable.
	if !strings.Contains(injectedPath, "/usr/bin") {
		t.Fatalf("injected PATH %q dropped the original PATH", injectedPath)
	}
}

// TestResolveAgentCommand_ReturnsClearErrorWhenMissing ensures that when the
// command genuinely cannot be found anywhere, the caller gets a single
// actionable error mentioning the command name (rather than a bare exec error).
func TestResolveAgentCommand_ReturnsClearErrorWhenMissing(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin:/usr/sbin:/sbin")
	t.Setenv("NVM_DIR", "")
	t.Setenv("VOLTA_HOME", "")
	prev := extraCandidateDirs
	t.Cleanup(func() { extraCandidateDirs = prev })
	extraCandidateDirs = nil

	agent := Agent{Command: "this-command-does-not-exist-anywhere-xyz"}
	err := resolveAgentCommand(&agent)
	if err == nil {
		t.Fatalf("expected error, got nil; agent.Env = %v", agent.Env)
	}
	if !strings.Contains(err.Error(), "this-command-does-not-exist-anywhere-xyz") {
		t.Fatalf("error must mention the missing command; got %v", err)
	}
}
