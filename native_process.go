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

// nativeProcessWait retains the one owned Wait result across cleanup retries.
type nativeProcessWait struct {
	once sync.Once
	done chan struct{}
	err  error
}

func stopNativeProcess(ctx context.Context, cmd *exec.Cmd, stdin io.Closer, wait *nativeProcessWait) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if stdin != nil {
		_ = stdin.Close()
	}
	wait.once.Do(func() {
		wait.done = make(chan struct{})
		go func() { wait.err = cmd.Wait(); close(wait.done) }()
	})
	pgid := processGroupIDAfterStart(cmd)
	for _, stage := range []struct {
		duration time.Duration
		signal   syscall.Signal
	}{
		{1500 * time.Millisecond, syscall.SIGTERM},
		{time.Second, syscall.SIGKILL},
		{3 * time.Second, 0},
	} {
		timer := time.NewTimer(stage.duration)
		select {
		case <-wait.done:
			timer.Stop()
			_ = signalProcessTree(pgid, nil, syscall.SIGTERM)
			var exit *exec.ExitError
			if wait.err != nil && !errors.As(wait.err, &exit) {
				return wrapError(ErrorProcess, "native.wait", "provider process Wait failed", wait.err)
			}
			return nil
		case <-ctx.Done():
			timer.Stop()
			_ = signalProcessTree(pgid, cmd.Process, syscall.SIGKILL)
			return wrapError(ErrorProcess, "native.cleanup", "provider process exit is not confirmed", ctx.Err())
		case <-timer.C:
			if stage.signal != 0 {
				_ = signalProcessTree(pgid, cmd.Process, stage.signal)
			}
		}
	}
	return wrapError(ErrorProcess, "native.cleanup", "provider process did not exit after kill", context.DeadlineExceeded)
}
