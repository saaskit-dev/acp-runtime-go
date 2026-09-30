package acpruntime

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// nativeProcessWait retains the one owned Wait result and process identities
// across cleanup retries. A failed Close never relinquishes that responsibility.
type nativeProcessWait struct {
	mu         sync.Mutex
	once       sync.Once
	done       chan struct{}
	err        error
	tree       nativeProcessTree
	captureErr error
	complete   bool
}

func (wait *nativeProcessWait) startLocked(cmd *exec.Cmd) {
	wait.once.Do(func() {
		wait.captureErr = wait.tree.capture(cmd.Process.Pid)
		wait.done = make(chan struct{})
		go func() { wait.err = cmd.Wait(); close(wait.done) }()
	})
}

func stopNativeProcess(ctx context.Context, cmd *exec.Cmd, stdin io.Closer, wait *nativeProcessWait) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	wait.mu.Lock()
	defer wait.mu.Unlock()
	if wait.complete {
		return nil
	}
	// Capture before Wait can reap the leader. configureProcessGroup makes its
	// PID the initial group ID, even when the leader has already exited.
	wait.startLocked(cmd)
	if stdin != nil {
		_ = stdin.Close()
	}
	if wait.captureErr != nil {
		return wrapError(ErrorProcess, "native.cleanup", "process ownership could not be captured", wait.captureErr)
	}
	started := time.Now()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	termSent, killSent := false, false
	for {
		leaderDone := false
		select {
		case <-wait.done:
			leaderDone = true
		default:
		}
		alive, err := wait.tree.alive()
		if err != nil {
			return wrapError(ErrorProcess, "native.cleanup", "descendant exit is not confirmed", err)
		}
		if leaderDone && !alive {
			var exit *exec.ExitError
			if wait.err != nil && !errors.As(wait.err, &exit) {
				return wrapError(ErrorProcess, "native.wait", "provider process Wait failed", wait.err)
			}
			wait.tree.release()
			wait.complete = true
			return nil
		}
		elapsed := time.Since(started)
		// If the leader exited, descendants get TERM immediately; successful leader
		// Wait is never itself evidence that TERM-ignoring descendants stopped.
		if !termSent && (leaderDone || elapsed >= 1500*time.Millisecond) {
			if err := wait.tree.signal(syscall.SIGTERM); err != nil {
				return wrapError(ErrorProcess, "native.cleanup", "terminate provider process", err)
			}
			termSent = true
		}
		if !killSent && elapsed >= 2500*time.Millisecond {
			if err := wait.tree.signal(syscall.SIGKILL); err != nil {
				return wrapError(ErrorProcess, "native.cleanup", "kill provider process", err)
			}
			killSent = true
		}
		if elapsed >= 5500*time.Millisecond {
			return wrapError(ErrorProcess, "native.cleanup", "provider process exit is not confirmed", context.DeadlineExceeded)
		}
		select {
		case <-ctx.Done():
			_ = wait.tree.signal(syscall.SIGKILL)
			return wrapError(ErrorProcess, "native.cleanup", "provider process exit is not confirmed", ctx.Err())
		case <-ticker.C:
		}
	}
}
