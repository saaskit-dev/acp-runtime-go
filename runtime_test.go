package acpruntime

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestRuntimeStartsSimulatorOverStdio(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cwd := t.TempDir()
	storage := t.TempDir()
	simulatorBin := buildSimulatorBinary(t)
	agent := Agent{
		Type:    LocalSimulatorAgentACPRegistryID,
		Command: simulatorBin,
		Args:    []string{"--auth-mode", "none", "--storage-dir", storage},
	}
	runtime := NewRuntime(NewStdioConnectionFactory(StdioFactoryOptions{}), RuntimeOptions{})
	session, err := runtime.StartSession(ctx, StartSessionOptions{Agent: agent, CWD: cwd})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	defer session.Close(context.Background())
	if session.Snapshot().Session.ID == "" {
		t.Fatalf("session id is empty")
	}
	if got := session.Metadata().CurrentModeID; got != "accept-edits" {
		t.Fatalf("CurrentModeID = %q, want accept-edits", got)
	}
	completion, err := session.Run(ctx, "Reply with the single word OK.")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if completion.OutputText != "OK" {
		t.Fatalf("OutputText = %q, want OK", completion.OutputText)
	}
}

func TestSimulatorWriteProducesOperation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cwd := t.TempDir()
	storage := t.TempDir()
	simulatorBin := buildSimulatorBinary(t)
	agent := Agent{
		Type:    LocalSimulatorAgentACPRegistryID,
		Command: simulatorBin,
		Args:    []string{"--auth-mode", "none", "--storage-dir", storage},
	}
	runtime := NewRuntime(NewStdioConnectionFactory(StdioFactoryOptions{}), RuntimeOptions{})
	session, err := runtime.StartSession(ctx, StartSessionOptions{Agent: agent, CWD: cwd})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	defer session.Close(context.Background())
	if _, err := session.Run(ctx, "write notes.txt hello"); err != nil {
		t.Fatalf("Run(write) error = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(cwd, "notes.txt"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("notes.txt = %q, want hello", string(data))
	}
	ops := session.Operations()
	if len(ops) == 0 {
		t.Fatalf("expected operation")
	}
	if ops[0].Kind != "write_file" {
		t.Fatalf("operation kind = %q, want write_file", ops[0].Kind)
	}
}

func buildSimulatorBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "acp-simulator-agent")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/acp-simulator-agent")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build simulator failed: %v\n%s", err, string(output))
	}
	return bin
}

func TestCommandsBuild(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, item := range []struct {
		name string
		pkg  string
	}{
		{name: "acp-runtime", pkg: "./cmd/acp-runtime"},
		{name: "acp-simulator-agent", pkg: "./cmd/acp-simulator-agent"},
		{name: "acp-harness", pkg: "./cmd/acp-harness"},
	} {
		out := filepath.Join(t.TempDir(), item.name)
		cmd := exec.CommandContext(ctx, "go", "build", "-o", out, item.pkg)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go build %s failed: %v\n%s", item.pkg, err, string(output))
		}
	}
}
