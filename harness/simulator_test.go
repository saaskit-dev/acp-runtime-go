package harness

import (
	"context"
	acp "github.com/saaskit-dev/acp-runtime-go"
	"github.com/saaskit-dev/acp-runtime-go/simulator"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHarnessSimulatorProcess(t *testing.T) {
	if os.Getenv("HARNESS_SIMULATOR_PROCESS") != "1" {
		return
	}
	err := simulator.RunStdio(context.Background(), os.Stdin, os.Stdout, simulator.Options{AuthMode: simulator.AuthOptional, StorageDir: os.Getenv("HARNESS_SIMULATOR_STORAGE")})
	if err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}
func simulatorAgent(t *testing.T) acp.Agent {
	return acp.Agent{Type: acp.LocalSimulatorAgentACPRegistryID, Command: os.Args[0], Args: []string{"-test.run=^TestHarnessSimulatorProcess$"}, Env: map[string]string{"HARNESS_SIMULATOR_PROCESS": "1", "HARNESS_SIMULATOR_STORAGE": t.TempDir()}}
}
func TestAllDeclaredSimulatorCases(t *testing.T) {
	paths, err := filepath.Glob("cases/*.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 32 {
		t.Fatalf("case audit expected 32, got %d", len(paths))
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			c, err := LoadCase(path)
			if err != nil {
				t.Fatal(err)
			}
			result, err := (Runner{Agent: simulatorAgent(t), CWD: t.TempDir()}).Run(ctx, c)
			if err != nil {
				t.Fatalf("%s: %v", result.Status, err)
			}
			if result.Status != "PASS" && result.Status != "SKIPPED" {
				t.Fatalf("unexpected %s", result.Status)
			}
			if result.Status == "PASS" && len(result.Transcript) == 0 {
				t.Fatal("PASS without wire evidence")
			}
			t.Logf("%s: %s", result.Status, result.Reason)
		})
	}
}
func TestWaitRequiresRealEvent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c := Case{ID: "missing-event", Steps: []CaseStep{{Type: "session-new"}, {Type: "session-prompt", Prompt: "OK"}, {Type: "wait-for-event", EventType: "not-real", TimeoutMS: 20}}}
	if _, err := (Runner{Agent: simulatorAgent(t), CWD: t.TempDir()}).Run(ctx, c); err == nil {
		t.Fatal("missing event passed")
	}
}

func TestSimulatorDenialDoesNotWrite(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	runtime := acp.NewRuntime(nil, acp.RuntimeOptions{})
	defer runtime.Close(context.Background())
	cwd := t.TempDir()
	session, err := runtime.StartSession(ctx, acp.StartSessionOptions{Agent: simulatorAgent(t), CWD: cwd})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cwd, "must-not-exist.txt")
	if _, err = session.Run(ctx, "/write "+path+" forbidden"); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("denied write touched filesystem: %v", err)
	}
}
func TestSimulatorCannotFabricateTerminalSuccess(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	runtime := acp.NewRuntime(nil, acp.RuntimeOptions{})
	defer runtime.Close(context.Background())
	session, err := runtime.StartSession(ctx, acp.StartSessionOptions{Agent: simulatorAgent(t), CWD: t.TempDir(), Handlers: acp.AuthorityHandlers{Permission: func(_ acp.Context, req acp.PermissionRequest) (acp.PermissionDecision, error) {
		for _, option := range req.Options {
			if option.Kind == "allow_once" {
				return acp.PermissionDecision{Outcome: "selected", OptionID: option.ID}, nil
			}
		}
		return acp.PermissionDecision{Outcome: "cancelled"}, nil
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if completion, err := session.Run(ctx, "/bash pwd"); err == nil {
		t.Fatalf("terminal without negotiated host support passed: %+v", completion)
	}
}
