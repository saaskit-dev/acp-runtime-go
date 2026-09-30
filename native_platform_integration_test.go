package acpruntime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNativePlatformConfigReadbackAndObservation(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	runtime := NewRuntime(nil, RuntimeOptions{})
	defer runtime.Close(context.Background())
	var mu sync.Mutex
	methods := map[string]int{}
	runtime.SetConnectionObserver(func(_ context.Context, input ConnectionFactoryInput, handle *ConnectionHandle) {
		if input.Agent.Type != CodexNativeRegistryID {
			t.Errorf("observer agent = %s", input.Agent.Type)
		}
		var boot map[string]any
		_ = json.Unmarshal([]byte(input.Agent.Env["CODEX_CONFIG"]), &boot)
		if boot["sandbox_mode"] != "read-only" || boot["instructions"] != "test prompt authority" {
			t.Error("native Codex lost sandbox or system prompt authority")
		}
		handle.Connection.SetRawMessageObserver(func(direction string, raw json.RawMessage) {
			var envelope struct {
				Method string `json:"method"`
			}
			if direction == "outbound" && json.Unmarshal(raw, &envelope) == nil && envelope.Method != "" {
				mu.Lock()
				methods[envelope.Method]++
				mu.Unlock()
			}
		})
	})
	agent := CreateCodexNativeAgent(Agent{Command: os.Args[0], Args: []string{"-test.run=TestFakeCodexAppServer", "--"}, Env: map[string]string{"GO_WANT_HELPER_PROCESS": "1"}})
	session, err := runtime.StartSession(ctx, StartSessionOptions{
		Agent: agent, CWD: t.TempDir(),
		AgentConfig:   &AgentConfig{Model: "test-model", Sandbox: "read-only"},
		InitialConfig: InitialConfig{Model: "test-model"},
		Meta:          map[string]any{SystemPromptMetaKey: "test prompt authority"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if state := session.Snapshot().RawConfig["model"]; state != "test-model" {
		t.Fatalf("model readback = %#v", state)
	}
	for range 2 {
		if _, err := session.Run(ctx, "hello"); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	for _, method := range []string{"initialize", "session/new", "session/prompt"} {
		if methods[method] == 0 {
			t.Errorf("native observer missed %s", method)
		}
	}
	if methods["session/prompt"] != 2 {
		t.Errorf("prompt observations = %d", methods["session/prompt"])
	}
}

func TestNativeClaudeToolAndHostAuthorityFlags(t *testing.T) {
	engine := &claudeNativeEngine{procs: map[string]*claudeProc{}}
	id, err := engine.NewSession(t.Context(), nativeEngineOptions{}, NewSessionRequest{Meta: map[string]any{
		"model": "test-model", "mode": "bypassPermissions",
		"claudeCode": map[string]any{"options": map[string]any{
			"tools": []any{}, "settingSources": []any{},
			"plugins":         []any{map[string]any{"type": "local", "path": "/test/plugin"}},
			"disallowedTools": []any{"Task", "Agent"},
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	proc, _ := engine.proc(id)
	args, err := proc.buildArgs()
	if err != nil {
		t.Fatal(err)
	}
	for flag, want := range map[string]string{"--session-id": id, "--model": "test-model", "--tools": "", "--setting-sources": "", "--plugin-dir": "/test/plugin", "--disallowedTools": "Task,Agent"} {
		found := false
		for index := range len(args) - 1 {
			if args[index] == flag && args[index+1] == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing %s=%q in %v", flag, want, args)
		}
	}
	state := engine.SessionState(id)
	if state.Modes == nil || state.Modes.CurrentModeID != "bypassPermissions" || state.Models.CurrentModelID != "test-model" {
		t.Fatalf("native state = %+v", state)
	}
}

func TestNativeClaudeResumeRetainsAuthority(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	agent := CreateClaudeCodeNativeAgent(Agent{Command: os.Args[0], Args: []string{"-test.run=TestFakeClaudeStreamJSON", "--"}, Env: map[string]string{"GO_WANT_HELPER_PROCESS": "1"}})
	opts := nativeEngineOptions{Agent: agent, CWD: t.TempDir()}
	engine := &claudeNativeEngine{}
	if err := engine.Start(ctx, opts); err != nil {
		t.Fatal(err)
	}
	defer engine.Close(context.Background())
	meta := map[string]any{"model": "test-model", SystemPromptMetaKey: "restore prompt", "claudeCode": map[string]any{"options": map[string]any{"disallowedTools": []any{"Task", "Agent"}}}}
	id, _ := newClaudeNativeSessionID()
	if _, err := engine.LoadSession(ctx, opts, LoadSessionRequest{SessionID: id, Meta: meta}); err != nil {
		t.Fatal(err)
	}
	proc, _ := engine.proc(id)
	if !reflect.DeepEqual(proc.pendingReq.Meta, meta) || engine.SessionState(id).Models.CurrentModelID != "test-model" {
		t.Fatal("resume dropped model / prompt / tool authority")
	}
}

type nativeCloseFailureEngine struct{ attempts int }

func (*nativeCloseFailureEngine) Name() string                                     { return "test" }
func (*nativeCloseFailureEngine) Start(context.Context, nativeEngineOptions) error { return nil }
func (*nativeCloseFailureEngine) NewSession(context.Context, nativeEngineOptions, NewSessionRequest) (string, error) {
	return "test", nil
}
func (*nativeCloseFailureEngine) LoadSession(context.Context, nativeEngineOptions, LoadSessionRequest) (string, error) {
	return "test", nil
}
func (*nativeCloseFailureEngine) Prompt(context.Context, nativeEngineOptions, string, []ContentBlock) (nativeTurnResult, error) {
	return nativeTurnResult{}, nil
}
func (*nativeCloseFailureEngine) SessionConfigOptions() []SessionConfigOption         { return nil }
func (*nativeCloseFailureEngine) Cancel(context.Context, nativeEngineOptions, string) {}
func (engine *nativeCloseFailureEngine) Close(context.Context) error {
	engine.attempts++
	if engine.attempts == 1 {
		return errors.New("process exit not confirmed")
	}
	return nil
}

func TestNativeDisposalPropagatesFailureAndRetries(t *testing.T) {
	engine := &nativeCloseFailureEngine{}
	handle, err := newNativeBridgeConnection(t.Context(), ConnectionFactoryInput{}, func() nativeEngine { return engine })
	if err != nil {
		t.Fatal(err)
	}
	if err := handle.Dispose(t.Context()); err == nil || !strings.Contains(err.Error(), "not confirmed") {
		t.Fatalf("cleanup error = %v", err)
	}
	if err := handle.Dispose(t.Context()); err != nil || engine.attempts != 2 {
		t.Fatalf("cleanup retry = %v, attempts = %d", err, engine.attempts)
	}
}
