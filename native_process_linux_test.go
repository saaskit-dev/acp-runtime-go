//go:build linux

package acpruntime

import (
	"context"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestNativeProcessTreeHelper(t *testing.T) {
	mode := os.Getenv("ACP_NATIVE_TREE_HELPER")
	if mode == "" {
		return
	}
	if mode == "child" {
		signal.Ignore(syscall.SIGTERM)
		_ = os.WriteFile(os.Getenv("ACP_NATIVE_TREE_READY"), []byte(strconv.Itoa(os.Getpid())), 0600)
		for {
			time.Sleep(time.Hour)
		}
	}
	child := exec.Command(os.Args[0], "-test.run=^TestNativeProcessTreeHelper$")
	child.Env = append(os.Environ(), "ACP_NATIVE_TREE_HELPER=child")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(os.Getenv("ACP_NATIVE_TREE_READY")); err == nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	// Exiting the leader leaves a real TERM-ignoring child in its inherited group.
}

func TestNativeCleanupWaitsForTermIgnoringDescendant(t *testing.T) {
	for _, cancelFirst := range []bool{false, true} {
		name := "term-then-kill"
		if cancelFirst {
			name = "cancel-then-retry"
		}
		t.Run(name, func(t *testing.T) { testNativeCleanupDescendant(t, cancelFirst) })
	}
}
func testNativeCleanupDescendant(t *testing.T, cancelFirst bool) {
	ready := filepath.Join(t.TempDir(), "child.pid")
	cmd := exec.Command(os.Args[0], "-test.run=^TestNativeProcessTreeHelper$")
	cmd.Env = append(os.Environ(), "ACP_NATIVE_TREE_HELPER=leader", "ACP_NATIVE_TREE_READY="+ready)
	configureProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	var pid int
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(ready); err == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
			break
		}
		time.Sleep(time.Millisecond)
	}
	if pid == 0 {
		_ = cmd.Process.Kill()
		t.Fatal("child did not start")
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	var wait nativeProcessWait
	// Force the first cleanup to time out; responsibility and identities survive.
	if cancelFirst {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := stopNativeProcess(ctx, cmd, nil, &wait); err == nil {
			t.Fatal("cancelled cleanup falsely succeeded")
		}
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 7*time.Second)
	defer cancel2()
	if err := stopNativeProcess(ctx2, cmd, nil, &wait); err != nil {
		t.Fatal(err)
	}
	processes, err := nativeProcessIdentities()
	if err != nil {
		t.Fatal(err)
	}
	if process, ok := processes[pid]; ok && process.state != "Z" && process.state != "X" {
		t.Fatalf("TERM-ignoring child still active: %+v", process)
	}
	if !wait.complete {
		t.Fatal("cleanup not retained as complete")
	}
	if err := stopNativeProcess(context.Background(), cmd, nil, &wait); err != nil {
		t.Fatal(err)
	}
}
