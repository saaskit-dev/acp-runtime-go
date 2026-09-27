package acpruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

type StdioFactoryOptions struct {
	Stderr       string
	OnACPMessage func(direction string, message []byte)
	// OnProcessExit, when set, fires once after the agent process has fully
	// exited — natural death or teardown — with the Wait error (nil on a clean
	// exit) and the captured stderr tail (empty unless Stderr == ""). It lets
	// hosts log WHY an agent vanished; mid-turn RPC failures surface to callers
	// as wrapped io.ErrClosedPipe errors carrying the transport cause.
	OnProcessExit func(err error, stderrTail string)
}

func NewStdioConnectionFactory(options StdioFactoryOptions) ConnectionFactory {
	return func(ctx context.Context, input ConnectionFactoryInput) (ConnectionHandle, error) {
		if input.Agent.Command == "" {
			return ConnectionHandle{}, &RuntimeError{Kind: ErrorProcess, Op: "stdio.spawn", Msg: "agent command is empty"}
		}
		// Resolve the agent command against common node install dirs before we
		// try to exec it. GUI app launches inherit a minimal PATH that does not
		// include Homebrew/nvm/volta; without this, `npm`-based agents fail to
		// spawn with "executable file not found in $PATH". No-op when the
		// command is already on PATH.
		if err := resolveAgentCommand(&input.Agent); err != nil {
			return ConnectionHandle{}, wrapError(ErrorProcess, "stdio.spawn", "failed to resolve agent command", err)
		}
		cmdCtx := context.WithoutCancel(ctx)
		cmd := exec.CommandContext(cmdCtx, input.Agent.Command, input.Agent.Args...)
		configureProcessGroup(cmd)
		cmd.Dir = input.CWD
		cmd.Env = envSlice(input.Agent.Env)
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return ConnectionHandle{}, err
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return ConnectionHandle{}, err
		}
		var stderr bytes.Buffer
		switch options.Stderr {
		case "inherit":
			cmd.Stderr = os.Stderr
		case "ignore":
			cmd.Stderr = io.Discard
		default:
			cmd.Stderr = &tailWriter{limit: 4096, buf: &stderr}
		}
		if err := cmd.Start(); err != nil {
			return ConnectionHandle{}, wrapError(ErrorProcess, "stdio.spawn", "failed to spawn ACP stdio process", err)
		}
		processGroupID := processGroupIDAfterStart(cmd)
		peerOptions := PeerOptions{}
		if options.OnACPMessage != nil {
			peerOptions.OnRawMessage = func(direction string, message json.RawMessage) {
				options.OnACPMessage(direction, message)
			}
		}
		peer := NewPeer(stdout, stdin, peerOptions)
		conn := NewConnectionWithObservability(peer, input.Client, input.Observability)
		startCtx, cancelStart := context.WithCancel(context.WithoutCancel(ctx))
		done := make(chan error, 1)
		// cmd.Wait may run exactly once. waitDone closes after it returns and
		// waitErr memoizes the result, so the teardown path and the process-exit
		// monitor below can both observe the outcome regardless of who reads
		// first — a value-carrying channel would let the first reader starve
		// the second.
		var (
			waitOnce sync.Once
			waitDone = make(chan struct{})
			waitErr  error
		)
		doWait := func() <-chan struct{} {
			waitOnce.Do(func() { go func() { waitErr = cmd.Wait(); close(waitDone) }() })
			return waitDone
		}
		waitResult := func() error { <-doWait(); return waitErr }
		go func() {
			done <- peer.Start(startCtx)
			// The read loop only ends when the child's stdout closes, i.e. the
			// process is going down (naturally or via teardown). Report the
			// final Wait result + stderr tail once available.
			if options.OnProcessExit != nil {
				options.OnProcessExit(waitResult(), stderr.String())
			}
		}()
		var teardownOnce sync.Once
		teardownDone := make(chan struct{})
		var teardownErr error
		startTeardown := func() {
			teardownOnce.Do(func() {
				go func() {
					defer close(teardownDone)
					cancelStart()
					_ = stdin.Close()
					waitDone := doWait()
					select {
					case <-waitDone:
						if err := waitResult(); err != nil && !errors.Is(err, context.Canceled) {
							teardownErr = err
						}
					case <-time.After(1500 * time.Millisecond):
						if cmd.Process != nil {
							_ = signalProcessTree(processGroupID, cmd.Process, syscall.SIGTERM)
						}
						select {
						case <-waitDone:
							_ = signalProcessTree(processGroupID, nil, syscall.SIGTERM)
							if err := waitResult(); err != nil && !errors.Is(err, context.Canceled) {
								teardownErr = err
							}
						case <-time.After(time.Second):
							if cmd.Process != nil {
								_ = signalProcessTree(processGroupID, cmd.Process, syscall.SIGKILL)
							}
							// Hard-cap the final Wait after SIGKILL so a stuck reaper
							// cannot pin the teardown goroutine forever. Callers that
							// timed out earlier still cannot orphan the only cleanup
							// attempt; we just stop waiting for an unkillable tree.
							select {
							case <-waitDone:
								_ = signalProcessTree(processGroupID, nil, syscall.SIGKILL)
								if err := waitResult(); err != nil && !errors.Is(err, context.Canceled) {
									teardownErr = fmt.Errorf("agent process required forced teardown: %w; stderr tail: %s", err, stderr.String())
								} else {
									teardownErr = fmt.Errorf("agent process required forced teardown; stderr tail: %s", stderr.String())
								}
							case <-time.After(3 * time.Second):
								_ = signalProcessTree(processGroupID, nil, syscall.SIGKILL)
								teardownErr = fmt.Errorf("agent process did not exit after SIGKILL; stderr tail: %s", stderr.String())
							}
						}
					}
					peer.Close()
					select {
					case <-done:
					default:
					}
				}()
			})
		}
		dispose := func(ctx context.Context) error {
			startTeardown()
			select {
			case <-teardownDone:
				return teardownErr
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return ConnectionHandle{Connection: conn, Dispose: dispose}, nil
	}
}

type tailWriter struct {
	limit int
	buf   *bytes.Buffer
}

func (w *tailWriter) Write(p []byte) (int, error) {
	n, err := w.buf.Write(p)
	if w.buf.Len() > w.limit {
		data := append([]byte(nil), w.buf.Bytes()...)
		w.buf.Reset()
		_, _ = w.buf.Write(data[len(data)-w.limit:])
	}
	return n, err
}
