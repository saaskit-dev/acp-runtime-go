package acpruntime

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// minimalGUIPath returns the PATH value a GUI app inherits on each platform
// when it is NOT launched from a login shell. These are the values that
// historically break "npm not found" for ACP agents.
func minimalGUIPath(t *testing.T) string {
	t.Helper()
	switch runtime.GOOS {
	case "windows":
		// Windows GUI apps inherit the system PATH but in stripped-down
		// scenarios (e.g. certain container/service contexts) this is what's
		// left. Use Windows path list separator.
		return `C:\Windows\System32;C:\Windows`
	case "darwin":
		return "/usr/bin:/bin:/usr/sbin:/sbin"
	default: // linux and other unix
		return "/usr/bin:/bin"
	}
}

// fakeAgentBinary creates a fake executable named `name` inside dir, with the
// correct extension and permissions for the host platform. Returns the full
// path that resolveAgentCommand should produce.
func fakeAgentBinary(t *testing.T, dir, name string) string {
	t.Helper()
	binPath := filepath.Join(dir, name)
	if runtime.GOOS == "windows" {
		binPath += ".exe"
		if err := os.WriteFile(binPath, []byte("fake"), 0o644); err != nil {
			t.Fatalf("WriteFile(%q) error = %v", binPath, err)
		}
		return binPath
	}
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\necho fake\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", binPath, err)
	}
	return binPath
}

// TestResolveAgentCommand_PassesThroughWhenOnPath covers the common case where
// the parent process already has the agent command on its PATH (e.g. ConnectMe
// launched from a login shell). resolveAgentCommand must be a no-op there: it
// must not rewrite Env or invent a PATH.
func TestResolveAgentCommand_PassesThroughWhenOnPath(t *testing.T) {
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
// failure on every supported platform: a GUI app inherits a stripped-down PATH
// that does not contain `npm`, even though npm is installed somewhere on the
// machine. resolveAgentCommand must locate the binary and rewrite the command
// to an absolute path.
func TestResolveAgentCommand_RecoversFromMinimalGUIPath(t *testing.T) {
	tmp := t.TempDir()
	wantPath := fakeAgentBinary(t, tmp, "npm")

	// Pin the parent PATH to the platform's GUI minimum so exec.LookPath fails.
	t.Setenv("PATH", minimalGUIPath(t))
	if _, err := exec.LookPath("npm"); err == nil {
		t.Fatalf("precondition failed: npm unexpectedly found on minimal PATH %q", os.Getenv("PATH"))
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
}

// TestResolveAgentCommand_ReturnsClearErrorWhenMissing ensures that when the
// command genuinely cannot be found anywhere, the caller gets a single
// actionable error mentioning the command name.
func TestResolveAgentCommand_ReturnsClearErrorWhenMissing(t *testing.T) {
	t.Setenv("PATH", minimalGUIPath(t))
	for _, env := range []string{"NVM_DIR", "VOLTA_HOME", "FNM_DIR", "N_PREFIX"} {
		t.Setenv(env, "")
	}
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

// TestResolveAgentCommand_WindowsResolvesExeWithoutExplicitSuffix verifies that
// on Windows, an agent Command of "npm" (no .exe) resolves to npm.exe when the
// candidate dir contains the .exe file. On non-Windows this is a no-op covered
// by TestResolveAgentCommand_RecoversFromMinimalGUIPath, so we skip.
func TestResolveAgentCommand_WindowsResolvesExeWithoutExplicitSuffix(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-only")
	}
	tmp := t.TempDir()
	wantPath := filepath.Join(tmp, "npm.exe")
	if err := os.WriteFile(wantPath, []byte("fake"), 0o644); err != nil {
		t.Fatalf("WriteFile error = %v", err)
	}
	t.Setenv("PATH", minimalGUIPath(t))
	prev := extraCandidateDirs
	t.Cleanup(func() { extraCandidateDirs = prev })
	extraCandidateDirs = []string{tmp}

	agent := Agent{Command: "npm"}
	if err := resolveAgentCommand(&agent); err != nil {
		t.Fatalf("resolveAgentCommand error = %v", err)
	}
	if agent.Command != wantPath {
		t.Fatalf("agent.Command = %q, want %q", agent.Command, wantPath)
	}
}

// TestNodeCandidateDirs_IncludesPlatformKeyLocations sanity-checks that
// nodeCandidateDirs() returns the documented key directories for the current
// platform. This guards against regressions where a refactor accidentally drops
// a critical directory (e.g. Homebrew on macOS, %AppData%\npm on Windows).
func TestNodeCandidateDirs_IncludesPlatformKeyLocations(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home dir: %v", err)
	}
	dirs := nodeCandidateDirs()
	contains := func(suffix string) bool {
		for _, d := range dirs {
			if strings.HasSuffix(filepath.ToSlash(d), suffix) {
				return true
			}
		}
		return false
	}

	switch runtime.GOOS {
	case "darwin":
		// Homebrew (Apple Silicon + Intel) + MacPorts.
		if !contains("/opt/homebrew/bin") {
			t.Errorf("missing /opt/homebrew/bin; dirs=%v", dirs)
		}
		if !contains("/usr/local/bin") {
			t.Errorf("missing /usr/local/bin; dirs=%v", dirs)
		}
	case "linux":
		if !contains("/usr/bin") {
			t.Errorf("missing /usr/bin; dirs=%v", dirs)
		}
		if !contains("/usr/local/bin") {
			t.Errorf("missing /usr/local/bin; dirs=%v", dirs)
		}
		if !contains(home + "/.local/bin") {
			t.Errorf("missing ~/.local/bin; dirs=%v", dirs)
		}
	case "windows":
		// %AppData%\npm (npm global bin) and Program Files\nodejs.
		appData := os.Getenv("AppData")
		if appData == "" {
			t.Skip("AppData not set")
		}
		if !contains(filepath.ToSlash(filepath.Join(appData, "npm"))) {
			t.Errorf("missing %%AppData%%/npm; dirs=%v", dirs)
		}
	default:
		t.Skipf("no platform assertions for GOOS=%s", runtime.GOOS)
	}
}

// TestNodeCandidateDirsWindows_KeyLocations verifies the Windows candidate
// generation logic directly, by calling nodeCandidateDirsWindows with a
// simulated Windows environment. This runs on every platform (the function is
// pure and env-driven), so we get real coverage of the Windows branch even when
// building/testing on macOS or Linux. Paths are compared after ToSlash since
// filepath.Join uses the host separator.
func TestNodeCandidateDirsWindows_KeyLocations(t *testing.T) {
	t.Setenv("ProgramFiles", `C:\Program Files`)
	t.Setenv("ProgramFiles(x86)", `C:\Program Files (x86)`)
	t.Setenv("AppData", `C:\Users\dev\AppData\Roaming`)
	t.Setenv("LocalAppData", `C:\Users\dev\AppData\Local`)
	t.Setenv("NVM_HOME", "")
	t.Setenv("NVM_SYMLINK", "")
	t.Setenv("VOLTA_HOME", "")

	dirs := nodeCandidateDirsWindows()
	// Normalize both the host separator AND any literal backslashes that came
	// from Windows-style env values, so the comparison is stable on every
	// platform (filepath.Join uses the host OS separator).
	contains := func(suffix string) bool {
		for _, d := range dirs {
			norm := strings.ReplaceAll(filepath.ToSlash(d), "\\", "/")
			if strings.HasSuffix(norm, suffix) {
				return true
			}
		}
		return false
	}
	wantSuffixes := []string{
		"Program Files/nodejs",
		"Program Files (x86)/nodejs",
		"AppData/Roaming/npm",   // npm global bin
		"AppData/Local/pnpm",    // pnpm global
		"AppData/Local/nvm",     // nvm-windows default
		"AppData/Local/fnm",     // fnm-windows
		"AppData/Local/Volta/bin", // volta default
	}
	for _, s := range wantSuffixes {
		if !contains(s) {
			t.Errorf("nodeCandidateDirsWindows() missing %q; dirs=%v", s, dirs)
		}
	}
}
