package acpruntime

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// extraCandidateDirs is overridden in tests to inject deterministic lookup
// locations. In production it stays empty and resolveAgentCommand relies on
// nodeCandidateDirs() instead, which enumerates the well-known node/npm install
// directories on the host.
var extraCandidateDirs []string

// resolveAgentCommand makes sure input.Agent.Command can be found on PATH
// before the stdio factory tries to exec it.
//
// Background: GUI apps launched from Finder/Dock/Spotlight on macOS inherit a
// minimal PATH (/usr/bin:/bin:/usr/sbin:/sbin) that does not include the
// directory where node-based CLIs (npm, npx, the ACP agent shims) are
// installed (Homebrew, nvm, volta, fnm, .n/bin, ...). The stdio factory passes
// os.Environ() through unchanged, so exec.LookPath fails and the spawn aborts
// with a confusing "executable file not found in $PATH" error even though the
// binary exists on the machine.
//
// If the command is already resolvable on the current PATH this is a no-op.
// Otherwise we scan a small set of well-known node install locations; when the
// binary is found there we record its directory on the agent's Env PATH so the
// downstream envSlice() merges it into the child process environment. We never
// mutate the global process environment.
func resolveAgentCommand(agent *Agent) error {
	if agent == nil || agent.Command == "" {
		return nil
	}

	// Fast path: the command resolves against the parent PATH already. Nothing
	// to do; leave Env untouched so callers that intentionally pass a minimal
	// Env are not surprised.
	if _, err := exec.LookPath(agent.Command); err == nil {
		return nil
	}

	// If the command is an absolute or relative path, exec.LookPath already
	// gave the definitive answer; do not second-guess it.
	if strings.ContainsRune(agent.Command, os.PathSeparator) {
		return fmt.Errorf("agent command %q not found: %w", agent.Command, exec.ErrNotFound)
	}

	// Test-injected candidates take precedence so unit tests stay deterministic
	// even on hosts that have a real node install under Homebrew/nvm.
	candidates := append([]string(nil), extraCandidateDirs...)
	candidates = append(candidates, nodeCandidateDirs()...)

	foundPath := ""
	for _, dir := range candidates {
		if dir == "" {
			continue
		}
		candidate := filepath.Join(dir, agent.Command)
		if runtime.GOOS == "windows" && !strings.HasSuffix(strings.ToLower(agent.Command), ".exe") {
			candidate += ".exe"
		}
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			foundPath = candidate
			break
		}
	}
	if foundPath == "" {
		return fmt.Errorf("agent command %q not found on PATH or in known node install directories (%s): %w",
			agent.Command, strings.Join(candidates, ", "), exec.ErrNotFound)
	}

	// Use the absolute path so exec.Command skips LookPath (which would consult
	// the parent PATH again and fail). We also inject the directory on Env PATH
	// so that child processes (npm spawns node, which shells out to git, etc.)
	// can still find their dependencies.
	parentPath := os.Getenv("PATH")
	newPath := filepath.Dir(foundPath) + string(os.PathListSeparator) + parentPath
	if agent.Env == nil {
		agent.Env = map[string]string{}
	}
	if _, ok := agent.Env["PATH"]; !ok {
		agent.Env["PATH"] = newPath
	}
	agent.Command = foundPath
	return nil
}

// nodeCandidateDirs returns the host's well-known directories that may hold
// node-based CLIs (npm/npx/node) when the parent process has a minimal PATH.
// The list is derived from environment variables when present (NVM_DIR,
// VOLTA_HOME, FNM_DIR, N_PREFIX) and falls back to common default locations
// for Homebrew, nvm, volta, fnm, and the .n version manager.
func nodeCandidateDirs() []string {
	home, _ := os.UserHomeDir()
	var dirs []string

	// Homebrew (Apple Silicon and Intel).
	dirs = append(dirs, "/opt/homebrew/bin", "/usr/local/bin")

	// nvm: $NVM_DIR/versions/node/<version>/bin plus the default install path.
	nvmDir := os.Getenv("NVM_DIR")
	if nvmDir == "" && home != "" {
		nvmDir = filepath.Join(home, ".nvm")
	}
	if nvmDir != "" {
		if entries, err := os.ReadDir(filepath.Join(nvmDir, "versions", "node")); err == nil {
			for _, e := range entries {
				if e.IsDir() {
					dirs = append(dirs, filepath.Join(nvmDir, "versions", "node", e.Name(), "bin"))
				}
			}
		}
		// nvm's own shim.
		dirs = append(dirs, filepath.Join(nvmDir, "versions", "node"))
	}

	// volta: $VOLTA_HOME/bin (default ~/.volta/bin).
	voltaHome := os.Getenv("VOLTA_HOME")
	if voltaHome == "" && home != "" {
		voltaHome = filepath.Join(home, ".volta")
	}
	if voltaHome != "" {
		dirs = append(dirs, filepath.Join(voltaHome, "bin"))
	}

	// fnm: $FNM_DIR or ~/.fnm/node-versions/<ver>/installation/bin, plus
	// ~/.local/share/fnm/aliases/default/bin on macOS.
	fnmDir := os.Getenv("FNM_DIR")
	if fnmDir == "" && home != "" {
		fnmDir = filepath.Join(home, ".fnm")
	}
	if fnmDir != "" {
		if entries, err := os.ReadDir(filepath.Join(fnmDir, "node-versions")); err == nil {
			for _, e := range entries {
				if e.IsDir() {
					dirs = append(dirs, filepath.Join(fnmDir, "node-versions", e.Name(), "installation", "bin"))
				}
			}
		}
	}
	if home != "" {
		dirs = append(dirs, filepath.Join(home, ".local", "share", "fnm", "aliases", "default", "bin"))
	}

	// .n (https://github.com/tj/n): N_PREFIX/bin (default /usr/local/n or
	// ~/.n), and the per-user ~/.n/bin.
	nPrefix := os.Getenv("N_PREFIX")
	if nPrefix == "" && home != "" {
		nPrefix = filepath.Join(home, ".n")
	}
	if nPrefix != "" {
		dirs = append(dirs, filepath.Join(nPrefix, "bin"))
	}
	if home != "" {
		dirs = append(dirs, filepath.Join(home, ".n", "bin"))
	}

	// User-level bin dirs that some setups rely on.
	if home != "" {
		dirs = append(dirs, filepath.Join(home, ".local", "bin"))
	}

	return dirs
}

// errAgentCommandNotFound is returned (wrapped) when no candidate resolves; it
// exists so callers can use errors.Is if needed.
var errAgentCommandNotFound = errors.New("agent command not found")
