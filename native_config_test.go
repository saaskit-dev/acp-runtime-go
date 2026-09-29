package acpruntime

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// TestClaudeNativeInitialConfigFlags: InitialConfig mode/model ride the
// standard set_mode/set_config_option RPCs and land on the deferred spawn's
// flags (the fake engine echoes them into its reply).
func TestClaudeNativeInitialConfigFlags(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") == "1" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	agent := CreateClaudeCodeNativeAgent(Agent{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestFakeClaudeStreamJSON", "--"},
		Env:     map[string]string{"GO_WANT_HELPER_PROCESS": "1"},
	})
	runtime := NewRuntime(nil, RuntimeOptions{})
	session, err := runtime.StartSession(ctx, StartSessionOptions{
		Agent: agent,
		CWD:   t.TempDir(),
		InitialConfig: InitialConfig{
			Mode:  "bypassPermissions",
			Model: "m1",
		},
	})
	if err != nil {
		t.Fatalf("StartSession with InitialConfig: %v", err)
	}
	defer func() { _ = session.Close(context.Background()) }()

	completion, err := session.Run(ctx, "hi")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(completion.OutputText, "MODEL=m1") || !strings.Contains(completion.OutputText, "MODE=bypassPermissions") {
		t.Fatalf("OutputText = %q, want MODEL=m1 and MODE=bypassPermissions echoes", completion.OutputText)
	}
}

// TestClaudeNativeMCPAndSettingsArgs: unit-level translation of unified
// MCPServers + AgentConfig settings into claude spawn flags and documents.
func TestClaudeNativeMCPAndSettingsArgs(t *testing.T) {
	e := &claudeNativeEngine{}
	e.opts = nativeEngineOptions{Agent: Agent{Command: "claude"}}
	proc := &claudeProc{eng: e}
	req := NewSessionRequest{
		MCPServers: []MCPServer{
			{Name: "fs", Command: "uvx", Args: []string{"mcp-server-fs"}, Env: []EnvVariable{{Name: "K", Value: "V"}}},
			{Name: "web", URL: "http://127.0.0.1:9/mcp", Headers: []HTTPHeader{{Name: "Authorization", Value: "Bearer t"}}},
		},
		Meta: map[string]any{
			"claudeCode": map[string]any{"options": map[string]any{
				"settings": map[string]any{"permissions": map[string]any{"allow": []any{"Bash"}}},
			}},
		},
	}
	proc.pendingReq = &req
	args, err := proc.buildArgs()
	if err != nil {
		t.Fatalf("buildArgs: %v", err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--mcp-config") || !strings.Contains(joined, "--strict-mcp-config") {
		t.Fatalf("args = %s, want mcp-config flags", joined)
	}
	if !strings.Contains(joined, "--settings") {
		t.Fatalf("args = %s, want --settings flag", joined)
	}
	var mcpFile string
	for i, arg := range args {
		if arg == "--mcp-config" && i+1 < len(args) {
			mcpFile = args[i+1]
		}
	}
	if mcpFile == "" {
		t.Fatalf("args = %s, want --mcp-config <file>", joined)
	}
	data, err := os.ReadFile(mcpFile)
	if err != nil {
		t.Fatalf("read mcp config: %v", err)
	}
	var parsed struct {
		MCPServers map[string]struct {
			Command string            `json:"command"`
			URL     string            `json:"url"`
			Type    string            `json:"type"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("mcp config not valid JSON: %v", err)
	}
	fsServer, ok := parsed.MCPServers["fs"]
	if !ok || fsServer.Command != "uvx" || len(fsServer.Args) != 1 || fsServer.Env["K"] != "V" {
		t.Fatalf("stdio server = %+v", fsServer)
	}
	webServer, ok := parsed.MCPServers["web"]
	if !ok || webServer.Type != "http" || webServer.URL != "http://127.0.0.1:9/mcp" {
		t.Fatalf("http server = %+v", webServer)
	}
	_ = os.Remove(mcpFile)
	_ = os.Remove(proc.settingsFile)
}

// TestCodexNativeMCPOverrideArgs: unified MCPServers flatten into dotted
// mcp_servers.* config overrides.
func TestCodexNativeMCPOverrideArgs(t *testing.T) {
	args := codexMCPOverrideArgs([]MCPServer{
		{Name: "fs", Command: "uvx", Args: []string{"mcp-server-fs"}, Env: []EnvVariable{{Name: "K", Value: "V"}}},
	})
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"mcp_servers.fs.command=\"uvx\"",
		"mcp_servers.fs.args=[\"mcp-server-fs\"]",
		"mcp_servers.fs.env={\"K\":\"V\"}",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args = %q, want %q", joined, want)
		}
	}
}
