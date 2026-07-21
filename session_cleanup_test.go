package acpruntime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestRuntimeInitialConfigFailureReturnsCleanupOutcome(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	storage := t.TempDir()
	runtime := NewRuntime(NewStdioConnectionFactory(StdioFactoryOptions{}), RuntimeOptions{
		StoredSessionsEnabled: true,
	})
	_, err := runtime.StartSession(ctx, StartSessionOptions{
		Agent: Agent{
			Type:    LocalSimulatorAgentACPRegistryID,
			Command: buildSimulatorBinary(t),
			Args:    []string{"--auth-mode", "none", "--storage-dir", storage},
		},
		CWD: t.TempDir(),
		InitialConfig: InitialConfig{
			Mode: "mode-that-was-not-advertised",
		},
	})
	if err == nil {
		t.Fatal("StartSession() error = nil, want initial config failure")
	}
	var runtimeErr *RuntimeError
	if !errors.As(err, &runtimeErr) {
		t.Fatalf("StartSession() error = %T, want *RuntimeError", err)
	}
	if runtimeErr.Kind != ErrorInitialConfig {
		t.Fatalf("RuntimeError.Kind = %q, want %q", runtimeErr.Kind, ErrorInitialConfig)
	}
	if runtimeErr.SessionID == "" {
		t.Fatal("RuntimeError.SessionID is empty after session/new succeeded")
	}
	if runtimeErr.CleanupStatus != CleanupSucceeded {
		t.Fatalf("RuntimeError.CleanupStatus = %q, want %q; cleanup error=%v", runtimeErr.CleanupStatus, CleanupSucceeded, runtimeErr.CleanupError)
	}
	if runtimeErr.CleanupError != nil {
		t.Fatalf("RuntimeError.CleanupError = %v, want nil", runtimeErr.CleanupError)
	}
}

// TestRuntimeResumeInitialConfigFailureReturnsSessionID verifies that a failed
// initial config after a successful session/resume reports the durable SessionID
// and marks durable cleanup as not_attempted (existing sessions must not be
// deleted). Connection teardown errors surface in CleanupError only.
func TestRuntimeResumeInitialConfigFailureReturnsSessionID(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	storage := t.TempDir()
	cwd := t.TempDir()
	agent := Agent{
		Type:    LocalSimulatorAgentACPRegistryID,
		Command: buildSimulatorBinary(t),
		Args:    []string{"--auth-mode", "none", "--storage-dir", storage},
	}
	runtime := NewRuntime(NewStdioConnectionFactory(StdioFactoryOptions{}), RuntimeOptions{
		StoredSessionsEnabled: true,
	})
	created, err := runtime.StartSession(ctx, StartSessionOptions{Agent: agent, CWD: cwd})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	defer created.Close(context.Background())
	sessionID := created.Snapshot().Session.ID
	if sessionID == "" {
		t.Fatal("created session ID is empty")
	}

	// Resume opens a second connection against the same durable SessionID. Keep
	// the creator process alive so the simulator still has the stored session.
	_, err = runtime.ResumeSession(ctx, ResumeSessionOptions{
		StartSessionOptions: StartSessionOptions{
			Agent: agent,
			CWD:   cwd,
			InitialConfig: InitialConfig{
				Mode: "mode-that-was-not-advertised",
			},
		},
		SessionID: sessionID,
	})
	if err == nil {
		t.Fatal("ResumeSession() error = nil, want initial config failure")
	}
	var runtimeErr *RuntimeError
	if !errors.As(err, &runtimeErr) {
		t.Fatalf("ResumeSession() error = %T, want *RuntimeError", err)
	}
	if runtimeErr.Kind != ErrorInitialConfig {
		t.Fatalf("RuntimeError.Kind = %q, want %q", runtimeErr.Kind, ErrorInitialConfig)
	}
	if runtimeErr.SessionID != sessionID {
		t.Fatalf("RuntimeError.SessionID = %q, want %q", runtimeErr.SessionID, sessionID)
	}
	if runtimeErr.CleanupStatus != CleanupNotAttempted {
		t.Fatalf("RuntimeError.CleanupStatus = %q, want %q; cleanup error=%v", runtimeErr.CleanupStatus, CleanupNotAttempted, runtimeErr.CleanupError)
	}
}

func TestSessionDeletePropagatesProviderCleanupFailure(t *testing.T) {
	providerReader, runtimeWriter := io.Pipe()
	runtimeReader, providerWriter := io.Pipe()
	runtimePeer := NewPeer(runtimeReader, runtimeWriter, PeerOptions{})
	providerPeer := NewPeer(providerReader, providerWriter, PeerOptions{})
	providerPeer.RegisterRequest("session/delete", func(_ context.Context, raw json.RawMessage) (any, error) {
		var req DeleteSessionRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, err
		}
		if req.SessionID != "session-delete-fails" {
			t.Fatalf("DeleteSessionRequest.SessionID = %q", req.SessionID)
		}
		return nil, errors.New("provider refused session/delete")
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = runtimePeer.Start(ctx) }()
	go func() { _ = providerPeer.Start(ctx) }()
	t.Cleanup(func() {
		runtimePeer.Close()
		providerPeer.Close()
		_ = providerReader.Close()
		_ = runtimeWriter.Close()
		_ = runtimeReader.Close()
		_ = providerWriter.Close()
	})

	driver := &acpSessionDriver{
		connection: NewConnection(runtimePeer, Client{}),
		sessionID:  "session-delete-fails",
		status:     "running",
	}
	err := driver.Delete(ctx)
	if err == nil || !strings.Contains(err.Error(), "provider refused session/delete") {
		t.Fatalf("Delete() error = %v, want provider cleanup failure", err)
	}
}
