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
// Background: GUI apps launched from Finder/Dock/Spotlight on macOS, the Start
// Menu on Windows, or a .desktop file on Linux inherit a minimal PATH that does
// not include the directory where node-based CLIs (npm, npx, the ACP agent
// shims) are installed (Homebrew, nvm, volta, fnm, nvm-windows, scoop, ...).
// The stdio factory passes os.Environ() through unchanged, so exec.LookPath
// fails and the spawn aborts with a confusing "executable file not found in
// $PATH" error even though the binary exists on the machine.
//
// If the command is already resolvable on the current PATH this is a no-op.
// Otherwise we scan a small set of well-known node install locations; when the
// binary is found there we rewrite Agent.Command to the absolute path (so
// exec.Command skips LookPath, which would consult the parent PATH again) and
// prepend the discovered directory to Agent.Env["PATH"] so child processes
// (npm spawns node, which shells out to git, etc.) can still find their
// dependencies. We never mutate the global process environment.
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
	if isExplicitPath(agent.Command) {
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
		if candidate := resolveInDir(dir, agent.Command); candidate != "" {
			foundPath = candidate
			break
		}
	}
	if foundPath == "" {
		return fmt.Errorf("agent command %q not found on PATH or in known node install directories (%s): %w",
			agent.Command, strings.Join(candidates, string(os.PathListSeparator)), exec.ErrNotFound)
	}

	// Rewrite Command to the absolute path so exec.Command skips LookPath (it
	// would otherwise consult the parent PATH again and fail). Inject the
	// directory at the front of Env PATH so child processes still resolve their
	// own dependencies (npm -> node -> git, etc.).
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

// isExplicitPath reports whether cmd looks like a path rather than a bare
// command name (i.e. it contains a path separator, or on Windows a drive
// letter). On Windows both `\` and `/` count as separators.
func isExplicitPath(cmd string) bool {
	if cmd == "" {
		return false
	}
	if strings.ContainsAny(cmd, `/\`) {
		return true
	}
	// Windows drive-relative like "C:foo".
	if runtime.GOOS == "windows" && len(cmd) >= 2 && cmd[1] == ':' {
		return true
	}
	return false
}

// resolveInDir looks for command inside dir, returning the absolute path to the
// executable if a match exists, or "" otherwise. On Windows it appends .exe
// when command has no extension and also honors PATHEXT for `cmd`-style lookup.
func resolveInDir(dir, command string) string {
	if command == "" {
		return ""
	}
	// If the command already carries an extension, check it verbatim.
	if filepath.Ext(command) != "" {
		candidate := filepath.Join(dir, command)
		if isExecutableFile(candidate) {
			return candidate
		}
		return ""
	}
	if runtime.GOOS == "windows" {
		// Try the common PATHEXT extensions, .exe first.
		exts := pathExtList()
		for _, ext := range exts {
			candidate := filepath.Join(dir, command+ext)
			if isExecutableFile(candidate) {
				return candidate
			}
		}
		return ""
	}
	// POSIX: bare command, no extension manipulation.
	candidate := filepath.Join(dir, command)
	if isExecutableFile(candidate) {
		return candidate
	}
	return ""
}

// pathExtList returns the ordered list of extensions to probe on Windows
// (derived from PATHEXT, with a sane default fallback).
func pathExtList() []string {
	pe := os.Getenv("PATHEXT")
	if pe == "" {
		return []string{".exe", ".cmd", ".bat"}
	}
	parts := strings.Split(pe, string(os.PathListSeparator))
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return []string{".exe", ".cmd", ".bat"}
	}
	return out
}

// isExecutableFile reports whether path is a regular file. On POSIX it also
// requires the executable bit, mirroring exec.LookPath semantics so a stale
// data file named "npm" does not get picked up.
func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	if runtime.GOOS == "windows" {
		// Windows has no executable bit; any regular file with an executable
		// extension (already enforced by the caller) is runnable.
		return true
	}
	return info.Mode().Perm()&0o111 != 0
}

// nodeCandidateDirs returns the host's well-known directories that may hold
// node-based CLIs (npm/npx/node) when the parent process has a minimal PATH.
// The list is platform-specific; see nodeCandidateDirsUnix and
// nodeCandidateDirsWindows for the per-OS details.
func nodeCandidateDirs() []string {
	if runtime.GOOS == "windows" {
		return nodeCandidateDirsWindows()
	}
	return nodeCandidateDirsUnix()
}

// nodeCandidateDirsUnix covers macOS and Linux. The list is derived from
// environment variables when present (NVM_DIR, VOLTA_HOME, FNM_DIR, N_PREFIX)
// and falls back to common default locations for Homebrew, MacPorts, nvm,
// volta, fnm, .n, bun, yarn, snap, and the user's ~/.local/bin.
func nodeCandidateDirsUnix() []string {
	home, _ := os.UserHomeDir()
	var dirs []string

	// Package-manager system locations.
	dirs = append(dirs,
		"/usr/local/bin", // Homebrew Intel, source installs, Linux user-local
		"/usr/bin",       // distro packages (Linux), system (macOS)
		"/opt/homebrew/bin", // Homebrew Apple Silicon
		"/opt/local/bin",    // MacPorts
	)

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
	}

	// volta: $VOLTA_HOME/bin (default ~/.volta/bin).
	voltaHome := os.Getenv("VOLTA_HOME")
	if voltaHome == "" && home != "" {
		voltaHome = filepath.Join(home, ".volta")
	}
	if voltaHome != "" {
		dirs = append(dirs, filepath.Join(voltaHome, "bin"))
	}

	// fnm: $FNM_DIR/node-versions/<ver>/installation/bin, plus
	// ~/.local/share/fnm/aliases/default/bin.
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

	// Yarn, Bun, snap, and the generic user-level bin dir.
	if home != "" {
		dirs = append(dirs,
			filepath.Join(home, ".yarn", "bin"),
			filepath.Join(home, ".bun", "bin"),
			filepath.Join(home, ".local", "bin"),
		)
	}
	dirs = append(dirs, "/snap/bin") // snap packages (Linux)

	return dirs
}

// nodeCandidateDirsWindows enumerates the directories where node-based CLIs
// land on Windows. GUI/service processes on Windows inherit the system PATH
// (which the official .msi installer augments), but user-scoped installs
// (nvm-windows, scoop, pnpm, fnm, volta) and per-user npm globals
// (%AppData%\npm) are frequently missing from a stripped-down PATH.
func nodeCandidateDirsWindows() []string {
	home, _ := os.UserHomeDir()
	var dirs []string

	// Official .msi installer (system-wide and 32-bit fallback).
	if pf := os.Getenv("ProgramFiles"); pf != "" {
		dirs = append(dirs, filepath.Join(pf, "nodejs"))
	}
	if pf86 := os.Getenv("ProgramFiles(x86)"); pf86 != "" {
		dirs = append(dirs, filepath.Join(pf86, "nodejs"))
	}

	// npm global bin: `npm install -g` puts shims here on Windows, and the
	// official installer adds this to PATH only for the installing user.
	if appData := os.Getenv("AppData"); appData != "" {
		dirs = append(dirs, filepath.Join(appData, "npm"))
	}

	// pnpm global install location.
	if localAppData := os.Getenv("LocalAppData"); localAppData != "" {
		dirs = append(dirs, filepath.Join(localAppData, "pnpm"))
	}

	// nvm-windows: %NVM_HOME% (defaults to %LocalAppData%\nvm) holds
	// nvm.exe; the active node version is symlinked under its root, so add the
	// root and let the PATH-style lookup find node.exe/npm.cmd there.
	if nvmHome := os.Getenv("NVM_HOME"); nvmHome != "" {
		dirs = append(dirs, nvmHome)
	} else if localAppData := os.Getenv("LocalAppData"); localAppData != "" {
		dirs = append(dirs, filepath.Join(localAppData, "nvm"))
	}
	if nvmSymlink := os.Getenv("NVM_SYMLINK"); nvmSymlink != "" {
		dirs = append(dirs, nvmSymlink)
	}

	// fnm-windows: %LocalAppData%\fnm_multishells is runtime-only; the stable
	// binary lives under %LocalAppData%\fnm and node under its default version.
	if localAppData := os.Getenv("LocalAppData"); localAppData != "" {
		dirs = append(dirs,
			filepath.Join(localAppData, "fnm"),
			filepath.Join(localAppData, "fnm_multishells"),
		)
	}

	// Volta for Windows.
	if voltaHome := os.Getenv("VOLTA_HOME"); voltaHome != "" {
		dirs = append(dirs, filepath.Join(voltaHome, "bin"))
	} else if localAppData := os.Getenv("LocalAppData"); localAppData != "" {
		dirs = append(dirs, filepath.Join(localAppData, "Volta", "bin"))
	}

	// Scoop: per-user shim dir + the nodejs app current symlink.
	if home != "" {
		dirs = append(dirs,
			filepath.Join(home, "scoop", "shims"),
			filepath.Join(home, "scoop", "apps", "nodejs", "current"),
		)
	}

	return dirs
}

// errAgentCommandNotFound is returned (wrapped) when no candidate resolves; it
// exists so callers can use errors.Is if needed.
var errAgentCommandNotFound = errors.New("agent command not found")
