package acpruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestConfigObjectParsingNeverDiscardsInvalidInput(t *testing.T) {
	for _, raw := range []string{"null", "[]", `"secret"`, "12", "{broken"} {
		t.Run(raw, func(t *testing.T) {
			env := map[string]string{"CODEX_CONFIG": raw}
			if _, err := buildCodexEnv(CodexConfig{Model: "x"}, env); err == nil {
				t.Fatal("Codex accepted non-object")
			}
			if _, err := injectCodexSystemPromptConfig(env, SystemPromptProjection{Text: "system"}); err == nil {
				t.Fatal("prompt overwrote invalid config")
			}
			dir := t.TempDir()
			file := filepath.Join(dir, "opencode.json")
			if err := os.WriteFile(file, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			if err := WriteOpenCodeConfig(dir, OpenCodeConfig{Model: "x"}); err == nil {
				t.Fatal("OpenCode accepted non-object")
			}
			data, _ := os.ReadFile(file)
			if string(data) != raw {
				t.Fatal("invalid original changed")
			}
		})
	}
}
func TestCodexDenyCannotBecomeWritableRoots(t *testing.T) {
	for _, typ := range []string{CodexACPRegistryID, CodexNativeRegistryID} {
		agent := Agent{Type: typ}
		_, _, err := prepareAgentSessionStart(ResolveAgentProfile(agent), StartSessionOptions{Agent: agent, AgentConfig: &AgentConfig{Permissions: PermissionConfig{Deny: []string{"/sensitive/do-not-write"}}}})
		var configErr *ConfigError
		if !errors.As(err, &configErr) {
			t.Fatalf("%s launched unsupported deny: %v", typ, err)
		}
		projected, _ := applyCodexAgentConfig(agent, AgentConfig{Permissions: PermissionConfig{Deny: []string{"/sensitive/do-not-write"}}})
		if strings.Contains(projected.Env["CODEX_CONFIG"], "/sensitive/do-not-write") {
			t.Fatal("deny became a writable root")
		}
	}
}
func TestCodexWritableRootsNestedAndMerged(t *testing.T) {
	enabled := true
	env, err := buildCodexEnv(CodexConfig{WritableRoots: []string{"/allowed"}, NetworkAccess: &enabled}, map[string]string{"CODEX_CONFIG": `{"sandbox_workspace_write":{"exclude_slash_tmp":true}}`})
	if err != nil {
		t.Fatal(err)
	}
	object, err := parseConfigObject([]byte(env["CODEX_CONFIG"]), "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := object["writable_roots"]; ok {
		t.Fatal("invalid top-level writable_roots")
	}
	nested := object["sandbox_workspace_write"].(map[string]any)
	if nested["exclude_slash_tmp"] != true || nested["network_access"] != true || !reflect.DeepEqual(nested["writable_roots"], []any{"/allowed"}) {
		t.Fatalf("nested merge=%v", nested)
	}
	for _, raw := range []string{`{"sandbox_workspace_write":null}`, `{"sandbox_workspace_write":[]}`, `{"sandbox_workspace_write":{"network_access":"true"}}`, `{"sandbox_workspace_write":{"deny":["/x"]}}`, `{"writable_roots":["/x"]}`} {
		if _, err := buildCodexEnv(CodexConfig{}, map[string]string{"CODEX_CONFIG": raw}); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
func TestOpenCodeAtomicWritePreservesMode(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "opencode.json")
	if err := os.WriteFile(file, []byte(`{"custom":{"keep":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := WriteOpenCodeConfig(dir, OpenCodeConfig{Model: "chosen"}); err != nil {
		t.Fatal(err)
	}
	stat, _ := os.Stat(file)
	if stat.Mode().Perm() != 0600 {
		t.Fatalf("mode=%v", stat.Mode())
	}
	data, _ := os.ReadFile(file)
	if !strings.Contains(string(data), "keep") {
		t.Fatal("original fields lost")
	}
}
func TestNativeInitialConfigResolvedBeforeStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runtime := NewRuntime(nil, RuntimeOptions{})
	defer runtime.Close(context.Background())
	agent := CreateCodexNativeAgent(Agent{Command: os.Args[0], Args: []string{"-test.run=^TestFakeCodexAppServer$", "--"}, Env: map[string]string{"GO_WANT_HELPER_PROCESS": "1"}})
	session, err := runtime.StartSession(ctx, StartSessionOptions{Agent: agent, CWD: t.TempDir(), InitialConfig: InitialConfig{Model: "explicit-not-provider-default"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := session.Snapshot().RawConfig["model"]; got != "explicit-not-provider-default" {
		t.Fatalf("model=%v", got)
	}
}
func TestOperationKindsRemainTruthful(t *testing.T) {
	profile := defaultAgentProfile()
	for input, want := range map[string]string{"execute": "execute_command", "execute_command": "execute_command", "read": "read_file", "read_file": "read_file", "edit": "write_file", "write_file": "write_file", "fetch": "network_request", "network_request": "network_request", "mystery": "unknown", "other": "unknown", "mcp_call": "mcp_call"} {
		if got := profile.MapOperationKind(input); got != want {
			t.Errorf("%s=%s want %s", input, got, want)
		}
	}
}

func TestUnsupportedSecurityConfigRejectsBeforeFactory(t *testing.T) {
	for _, typ := range []string{CodexACPRegistryID, CodexNativeRegistryID} {
		calls := 0
		runtime := NewRuntime(func(context.Context, ConnectionFactoryInput) (ConnectionHandle, error) {
			calls++
			return ConnectionHandle{}, errors.New("factory must not be reached")
		}, RuntimeOptions{})
		_, err := runtime.StartSession(context.Background(), StartSessionOptions{Agent: Agent{Type: typ}, AgentConfig: &AgentConfig{Permissions: PermissionConfig{Deny: []string{"/sensitive/do-not-write"}}}})
		var configErr *ConfigError
		if !errors.As(err, &configErr) || calls != 0 {
			t.Fatalf("%s unsafe launch: calls=%d error=%v", typ, calls, err)
		}
	}
}
func TestNativeConfigConflictsFailBeforeLaunch(t *testing.T) {
	agent := CreateCodexNativeAgent(Agent{})
	cases := []StartSessionOptions{
		{Agent: agent, AgentConfig: &AgentConfig{Sandbox: "read-only", Extra: map[string]any{"sandbox_mode": "danger-full-access"}}},
		{Agent: Agent{Type: CodexNativeRegistryID, Args: []string{"app-server", "--yolo"}}, AgentConfig: &AgentConfig{Sandbox: "read-only"}},
		{Agent: agent, Meta: map[string]any{"model": "meta-model"}, InitialConfig: InitialConfig{Model: "another"}},
		{Agent: Agent{Type: ClaudeCodeNativeRegistryID, Args: []string{"--model", "argv-model"}}, InitialConfig: InitialConfig{Model: "another"}},
		{Agent: agent, Meta: map[string]any{"model": false}},
	}
	for i, input := range cases {
		if _, _, err := prepareAgentSessionStart(ResolveAgentProfile(input.Agent), input); err == nil {
			t.Errorf("case %d accepted conflicting config", i)
		}
	}
}
