//go:build !linux

package acpruntime

import (
	"os"
	"syscall"
)

// On non-Linux platforms this tracker confirms only the owned leader. Full
// descendant containment needs platform-specific job/cgroup supervision; it is
// deliberately not inferred from the leader's Wait result.
type nativeProcessTree struct{ process *os.Process }

func (tree *nativeProcessTree) capture(pid int) error {
	process, err := os.FindProcess(pid)
	tree.process = process
	return err
}
func (tree *nativeProcessTree) signal(signal syscall.Signal) error {
	if tree.process == nil {
		return nil
	}
	return signalProcessTree(0, tree.process, signal)
}
func (tree *nativeProcessTree) alive() (bool, error) { return false, nil }
func (tree *nativeProcessTree) release() {
	if tree.process != nil {
		_ = tree.process.Release()
		tree.process = nil
	}
}
