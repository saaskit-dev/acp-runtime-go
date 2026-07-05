//go:build !windows

package acpruntime

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// configureProcessGroup puts the agent process in its own process group so we
// can later signal the whole tree (npm -> node -> children) on teardown. This
// is Unix-only; the Windows variant lives in stdio_proctree_windows.go.
func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// processGroupIDAfterStart returns the process group id of the started command,
// or 0 if it cannot be determined. Used to target the whole tree on teardown.
func processGroupIDAfterStart(cmd *exec.Cmd) int {
	if cmd.Process == nil {
		return 0
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		return 0
	}
	return pgid
}

// signalProcessTree delivers signal to the process tree rooted at pgid/process.
// It first targets direct descendants discovered via ps, then the whole group
// via the negative pgid, then falls back to signaling the leader itself.
func signalProcessTree(pgid int, process *os.Process, signal syscall.Signal) error {
	if process != nil {
		for _, pid := range descendantPIDs(process.Pid) {
			_ = syscall.Kill(pid, signal)
		}
	}
	if pgid > 0 {
		if err := syscall.Kill(-pgid, signal); err == nil {
			return nil
		}
	}
	if process == nil {
		return nil
	}
	return process.Signal(signal)
}

// descendantPIDs enumerates the descendant PIDs of rootPID using ps.
func descendantPIDs(rootPID int) []int {
	if rootPID <= 0 {
		return nil
	}
	output, err := exec.Command("ps", "-axo", "pid=,ppid=").Output()
	if err != nil {
		return nil
	}
	children := make(map[int][]int)
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		children[ppid] = append(children[ppid], pid)
	}
	var descendants []int
	stack := append([]int(nil), children[rootPID]...)
	for len(stack) > 0 {
		pid := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		descendants = append(descendants, pid)
		stack = append(stack, children[pid]...)
	}
	return descendants
}
