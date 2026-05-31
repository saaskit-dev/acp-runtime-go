package acpruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
		cmd := exec.CommandContext(ctx, input.Agent.Command, input.Agent.Args...)
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
			cmd.Stderr = io.Discard
		case "ignore":
			cmd.Stderr = io.Discard
		default:
			cmd.Stderr = &tailWriter{limit: 4096, buf: &stderr}
		}
		if err := cmd.Start(); err != nil {
			return ConnectionHandle{}, wrapError(ErrorProcess, "stdio.spawn", "failed to spawn ACP stdio process", err)
		}
		peerOptions := PeerOptions{}
		if options.OnACPMessage != nil {
			peerOptions.OnRawMessage = func(direction string, message json.RawMessage) {
				options.OnACPMessage(direction, message)
			}
		}
		peer := NewPeer(stdout, stdin, peerOptions)
		conn := NewConnection(peer, input.Client)
		startCtx, cancelStart := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			done <- peer.Start(startCtx)
		}()
		var disposeOnce sync.Once
		dispose := func(ctx context.Context) error {
			var disposeErr error
			disposeOnce.Do(func() {
				cancelStart()
				_ = stdin.Close()
				waitCh := make(chan error, 1)
				go func() { waitCh <- cmd.Wait() }()
				select {
				case err := <-waitCh:
					if err != nil && !errors.Is(err, context.Canceled) {
						disposeErr = err
					}
				case <-time.After(1500 * time.Millisecond):
					if cmd.Process != nil {
						_ = cmd.Process.Signal(syscall.SIGTERM)
					}
					select {
					case err := <-waitCh:
						if err != nil && !errors.Is(err, context.Canceled) {
							disposeErr = err
						}
					case <-time.After(time.Second):
						if cmd.Process != nil {
							_ = cmd.Process.Kill()
						}
						disposeErr = fmt.Errorf("agent process did not exit cleanly; stderr tail: %s", stderr.String())
					case <-ctx.Done():
						disposeErr = ctx.Err()
					}
				case <-ctx.Done():
					disposeErr = ctx.Err()
				}
				peer.Close()
				select {
				case <-done:
				default:
				}
			})
			return disposeErr
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
