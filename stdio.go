package acpruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"sync"
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
		stderrTail := &tailWriter{limit: 4096, buf: &stderr}
		switch options.Stderr {
		case "inherit":
			cmd.Stderr = os.Stderr
		case "ignore":
			cmd.Stderr = io.Discard
		default:
			cmd.Stderr = stderrTail
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
		conn := NewConnectionWithObservability(peer, input.Client, input.Observability)
		startCtx, cancelStart := context.WithCancel(context.WithoutCancel(ctx))
		var processWait nativeProcessWait
		var cleanupMu sync.Mutex
		var cleanupDone chan struct{}
		var cleanupErr error
		var cleanupComplete bool
		startTeardown := func() <-chan struct{} {
			cleanupMu.Lock()
			defer cleanupMu.Unlock()
			if cleanupComplete || cleanupDone != nil {
				return cleanupDone
			}
			cleanupDone = make(chan struct{})
			done := cleanupDone
			go func() {
				cancelStart()
				// Cleanup remains owned after a caller timeout. Failed attempts may be
				// retried using the same Wait result and captured process identities.
				cleanupCtx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
				defer cancel()
				err := stopNativeProcess(cleanupCtx, cmd, stdin, &processWait)
				peer.Close()
				cleanupMu.Lock()
				cleanupErr = err
				cleanupComplete = err == nil
				close(done)
				if err != nil {
					cleanupDone = nil
				}
				cleanupMu.Unlock()
			}()
			return done
		}
		go func() {
			_ = peer.Start(startCtx)
			// EOF can occur before descendants exit; take ownership of cleanup even
			// when the caller has not yet closed its session handle.
			_ = startTeardown()
			if options.OnProcessExit != nil {
				processWait.mu.Lock()
				processWait.startLocked(cmd)
				done := processWait.done
				processWait.mu.Unlock()
				<-done
				options.OnProcessExit(processWait.err, stderrTail.String())
			}
		}()
		dispose := func(ctx context.Context) error {
			done := startTeardown()
			select {
			case <-done:
				cleanupMu.Lock()
				err := cleanupErr
				cleanupMu.Unlock()
				return err
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return ConnectionHandle{Connection: conn, Dispose: dispose}, nil
	}
}

type tailWriter struct {
	mu    sync.Mutex
	limit int
	buf   *bytes.Buffer
}

func (w *tailWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.buf.Write(p)
	if w.buf.Len() > w.limit {
		data := append([]byte(nil), w.buf.Bytes()...)
		w.buf.Reset()
		_, _ = w.buf.Write(data[len(data)-w.limit:])
	}
	return n, err
}

func (w *tailWriter) String() string { w.mu.Lock(); defer w.mu.Unlock(); return w.buf.String() }
