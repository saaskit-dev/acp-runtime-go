package acpruntime

import (
	"context"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
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

func TestStdioProcessOutlivesStartContextUntilDispose(t *testing.T) {
	if os.Getenv("ACP_RUNTIME_STDIO_HELPER") == "wait-stdin" {
		_, _ = io.Copy(io.Discard, os.Stdin)
		return
	}

	factory := NewStdioConnectionFactory(StdioFactoryOptions{})
	startCtx, cancelStart := context.WithCancel(context.Background())
	handle, err := factory(startCtx, ConnectionFactoryInput{
		Agent: Agent{
			Command: os.Args[0],
			Args:    []string{"-test.run=TestStdioProcessOutlivesStartContextUntilDispose", "--"},
			Env:     map[string]string{"ACP_RUNTIME_STDIO_HELPER": "wait-stdin"},
		},
		CWD: ".",
	})
	if err != nil {
		t.Fatalf("factory() error = %v", err)
	}
	cancelStart()
	time.Sleep(100 * time.Millisecond)

	disposeCtx, cancelDispose := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelDispose()
	if err := handle.Dispose(disposeCtx); err != nil {
		t.Fatalf("Dispose() error after start context cancel = %v", err)
	}
}

func TestStdioDisposeSignalsProcessGroup(t *testing.T) {
	pidFile, err := os.CreateTemp(t.TempDir(), "child-pid-*")
	if err != nil {
		t.Fatalf("CreateTemp() error = %v", err)
	}
	_ = pidFile.Close()
	script := "sleep 30 & child=$!; parent_pgid=$(ps -o pgid= -p $$ | tr -d ' '); child_pgid=$(ps -o pgid= -p $child | tr -d ' '); echo \"$$ $child $parent_pgid $child_pgid\" > \"$ACP_RUNTIME_CHILD_PID_FILE\"; trap '' TERM; while true; do sleep 1; done"
	factory := NewStdioConnectionFactory(StdioFactoryOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	handle, err := factory(ctx, ConnectionFactoryInput{
		Agent: Agent{
			Command: "sh",
			Args:    []string{"-c", script},
			Env: map[string]string{
				"ACP_RUNTIME_CHILD_PID_FILE": pidFile.Name(),
			},
		},
		CWD: ".",
	})
	if err != nil {
		t.Fatalf("factory() error = %v", err)
	}
	processInfo := waitForProcessInfoFile(t, pidFile.Name())
	childPID := processInfo.childPID
	if !containsPID(descendantPIDs(processInfo.parentPID), childPID) {
		t.Fatalf("descendantPIDs(%d) did not include child %d: info=%+v ps=\n%s", processInfo.parentPID, childPID, processInfo, psForPIDs(processInfo.parentPID, processInfo.childPID))
	}
	t.Cleanup(func() {
		if processAlive(childPID) {
			_ = syscall.Kill(childPID, syscall.SIGKILL)
		}
	})

	disposeCtx, cancelDispose := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelDispose()
	_ = handle.Dispose(disposeCtx)
	deadline := time.Now().Add(3 * time.Second)
	for processAlive(childPID) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if processAlive(childPID) {
		t.Fatalf("child process remained alive after Dispose(): info=%+v ps=\n%s", processInfo, psForPIDs(processInfo.parentPID, processInfo.childPID))
	}
}

type testProcessInfo struct {
	parentPID  int
	childPID   int
	parentPGID int
	childPGID  int
}

func waitForProcessInfoFile(t *testing.T, path string) testProcessInfo {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil && len(data) > 0 {
			fields := strings.Fields(string(data))
			if len(fields) != 4 {
				t.Fatalf("child pid file = %q, want four fields", string(data))
			}
			var info testProcessInfo
			targets := []*int{&info.parentPID, &info.childPID, &info.parentPGID, &info.childPGID}
			for i, field := range fields {
				value, err := strconv.Atoi(field)
				if err != nil {
					t.Fatalf("child pid file = %q, field %d is not an int", string(data), i)
				}
				*targets[i] = value
			}
			return info
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("child pid file %s was not written", path)
	return testProcessInfo{}
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	output, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	if err != nil || len(output) == 0 {
		return false
	}
	return !strings.Contains(string(output), "Z")
}

func psForPIDs(pids ...int) string {
	args := []string{"-o", "pid,ppid,pgid,stat,command", "-p"}
	ids := make([]string, 0, len(pids))
	for _, pid := range pids {
		if pid > 0 {
			ids = append(ids, strconv.Itoa(pid))
		}
	}
	args = append(args, strings.Join(ids, ","))
	output, err := exec.Command("ps", args...).CombinedOutput()
	if err != nil {
		return string(output)
	}
	return string(output)
}

func containsPID(pids []int, target int) bool {
	for _, pid := range pids {
		if pid == target {
			return true
		}
	}
	return false
}
