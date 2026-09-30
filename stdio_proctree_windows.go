//go:build windows

package acpruntime

import (
	"os"
	"os/exec"
	"syscall"
)

// On Windows there is no portable equivalent of POSIX process groups reachable
// from Go without WMI/Job Object plumbing. Teardown therefore degrades to
// signaling only the agent leader process. Closing stdin is not evidence
// that descendants exited. Full tree containment is unsupported without a
// Windows Job Object; callers requiring it must provide external supervision.

func configureProcessGroup(cmd *exec.Cmd) {
	// No-op on Windows: SysProcAttr on Windows has no Setpgid equivalent.
}

func processGroupIDAfterStart(cmd *exec.Cmd) int {
	return 0
}

// signalProcessTree on Windows can only target the leader process. SIGTERM and
// SIGKILL are not real Windows signals; os.Process.Signal on Windows only
// supports SIGKILL (which maps to TerminateProcess). We translate SIGTERM to
// Kill to honor the caller's intent of forcing teardown.
func signalProcessTree(pgid int, process *os.Process, signal syscall.Signal) error {
	if process == nil {
		return nil
	}
	if signal == syscall.SIGKILL {
		return process.Kill()
	}
	// SIGTERM/SIGINT: best effort, fall back to Kill since Windows can't
	// deliver those signals. This matches the dispose path's escalation timer.
	if err := process.Kill(); err != nil {
		return err
	}
	return nil
}
