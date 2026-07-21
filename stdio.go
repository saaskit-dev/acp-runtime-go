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
		go func() {
			done <- peer.Start(startCtx)
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
					waitCh := make(chan error, 1)
					go func() { waitCh <- cmd.Wait() }()
					select {
					case err := <-waitCh:
						if err != nil && !errors.Is(err, context.Canceled) {
							teardownErr = err
						}
					case <-time.After(1500 * time.Millisecond):
						if cmd.Process != nil {
							_ = signalProcessTree(processGroupID, cmd.Process, syscall.SIGTERM)
						}
						select {
						case err := <-waitCh:
							_ = signalProcessTree(processGroupID, nil, syscall.SIGTERM)
							if err != nil && !errors.Is(err, context.Canceled) {
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
							case err := <-waitCh:
								_ = signalProcessTree(processGroupID, nil, syscall.SIGKILL)
								if err != nil && !errors.Is(err, context.Canceled) {
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
