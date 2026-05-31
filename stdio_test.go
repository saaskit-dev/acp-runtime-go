package acpruntime

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func TestStdioInheritForwardsStderr(t *testing.T) {
	if os.Getenv("ACP_RUNTIME_STDIO_HELPER") == "stderr" {
		_, _ = os.Stderr.WriteString("stderr-inherit-marker")
		return
	}

	originalStderr := os.Stderr
	readStderr, writeStderr, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe() error = %v", err)
	}
	os.Stderr = writeStderr
	t.Cleanup(func() {
		os.Stderr = originalStderr
		_ = readStderr.Close()
		_ = writeStderr.Close()
	})

	factory := NewStdioConnectionFactory(StdioFactoryOptions{Stderr: "inherit"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	handle, err := factory(ctx, ConnectionFactoryInput{
		Agent: Agent{
			Command: os.Args[0],
			Args:    []string{"-test.run=TestStdioInheritForwardsStderr", "--"},
			Env:     map[string]string{"ACP_RUNTIME_STDIO_HELPER": "stderr"},
		},
		CWD: ".",
	})
	if err != nil {
		t.Fatalf("factory() error = %v", err)
	}
	if err := handle.Dispose(ctx); err != nil {
		t.Fatalf("Dispose() error = %v", err)
	}
	if err := writeStderr.Close(); err != nil {
		t.Fatalf("stderr close error = %v", err)
	}
	data, err := io.ReadAll(readStderr)
	if err != nil {
		t.Fatalf("ReadAll(stderr) error = %v", err)
	}
	if !strings.Contains(string(data), "stderr-inherit-marker") {
		t.Fatalf("stderr output = %q, want marker", string(data))
	}
}
