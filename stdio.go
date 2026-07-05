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
	"strconv"
	"strings"
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
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
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
		processGroupID := 0
		if cmd.Process != nil {
			if pgid, err := syscall.Getpgid(cmd.Process.Pid); err == nil {
				processGroupID = pgid
			}
		}
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
					forcedTeardown := false
					if cmd.Process != nil {
						forcedTeardown = true
						_ = signalProcessTree(processGroupID, cmd.Process, syscall.SIGTERM)
					}
					select {
					case err := <-waitCh:
						if forcedTeardown {
							_ = signalProcessTree(processGroupID, nil, syscall.SIGTERM)
						}
						if err != nil && !errors.Is(err, context.Canceled) {
							disposeErr = err
						}
					case <-time.After(time.Second):
						if cmd.Process != nil {
							_ = signalProcessTree(processGroupID, cmd.Process, syscall.SIGKILL)
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
