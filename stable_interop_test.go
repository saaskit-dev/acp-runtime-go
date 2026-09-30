package acpruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func requireSchemaPython(t *testing.T) string {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("independent schema gate requires Python3")
	}
	if err := exec.Command(python, "-c", "import jsonschema").Run(); err != nil {
		t.Skip("independent schema gate requires jsonschema==4.26.0")
	}
	return python
}
func startIndependentAgent(t *testing.T, ctx context.Context, client Client, mode string) ConnectionHandle {
	t.Helper()
	cmd := exec.CommandContext(ctx, requireSchemaPython(t), "testdata/acp/stdio_contract_agent.py", mode)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	peer := NewPeer(stdout, stdin, PeerOptions{})
	conn := NewConnection(peer, client)
	go func() { _ = peer.Start(ctx) }()
	var once sync.Once
	var disposeErr error
	dispose := func(context.Context) error {
		once.Do(func() {
			peer.Close()
			_ = stdin.Close()
			_ = stdout.Close()
			disposeErr = cmd.Wait()
			if stderr.Len() > 0 {
				t.Errorf("independent peer diagnostics: %s", stderr.String())
			}
		})
		return disposeErr
	}
	t.Cleanup(func() { _ = dispose(context.Background()) })
	return ConnectionHandle{Connection: conn, Dispose: dispose}
}
func TestStableIndependentPythonWire(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	complete := make(chan ElicitationCompletion, 1)
	client := defaultClient(RuntimeOptions{}, AuthorityHandlers{Permission: func(_ Context, r PermissionRequest) (PermissionDecision, error) {
		switch r.Title {
		case "Reject operation":
			return defaultDenyPermissionDecision(r), nil
		case "Cancel operation":
			return PermissionDecision{Outcome: "cancelled"}, nil
		}
		return PermissionDecision{Outcome: "selected", OptionID: "approve-one"}, nil
	}, Elicitation: ElicitationHandlers{
		Form: func(Context, ElicitationRequest) (ElicitationResponse, error) {
			return ElicitationResponse{Action: "accept", Content: map[string]any{"color": "blue", "enabled": false}}, nil
		},
		URL: func(Context, ElicitationRequest) (ElicitationResponse, error) {
			return ElicitationResponse{Action: "accept"}, nil
		}, OnComplete: func(_ Context, event ElicitationCompletion) { complete <- event },
	}})
	h := startIndependentAgent(t, ctx, client, "normal")
	c := h.Connection
	if _, err := c.Initialize(ctx, InitializeRequest{ProtocolVersion: 1, ClientCapabilities: client.Capabilities}); err != nil {
		t.Fatal(err)
	}
	created, err := c.NewSession(ctx, NewSessionRequest{CWD: "/workspace"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetSessionConfigOption(ctx, SetSessionConfigOptionRequest{SessionID: created.SessionID, OptionID: "thinking", Value: false}); err != nil {
		t.Fatal(err)
	}
	updates := make(chan SessionNotification, 1)
	c.SetSessionUpdateHandler(func(_ context.Context, n SessionNotification) { updates <- n })
	if _, err := c.Prompt(ctx, PromptRequest{SessionID: created.SessionID, Prompt: []ContentBlock{{Type: "text", Text: "contract fixture"}}}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-complete:
		if got.Request.SessionID != created.SessionID {
			t.Fatal("completion scope lost")
		}
	case <-ctx.Done():
		t.Fatal("missing completion")
	}
	select {
	case got := <-updates:
		if got.Update.Used == nil || *got.Update.Used != 0 {
			t.Fatal("usage lost")
		}
	case <-ctx.Done():
		t.Fatal("missing usage")
	}
	loaded, err := c.LoadSession(ctx, LoadSessionRequest{SessionID: created.SessionID, CWD: "/workspace"})
	if err != nil || loaded.SessionID != created.SessionID {
		t.Fatalf("load %+v %v", loaded, err)
	}
	resumed, err := c.ResumeSession(ctx, ResumeSessionRequest{SessionID: created.SessionID, CWD: "/workspace"})
	if err != nil || resumed.SessionID != created.SessionID {
		t.Fatalf("resume %+v %v", resumed, err)
	}
	if err := c.CloseSession(ctx, CloseSessionRequest{SessionID: created.SessionID}); err != nil {
		t.Fatal(err)
	}
	if err := c.Logout(ctx, LogoutRequest{}); err != nil {
		t.Fatal(err)
	}
}
func TestStableIndependentTerminalAuthReconnect(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var starts atomic.Int32
	var logins atomic.Int32
	options := RuntimeOptions{TerminalAuthenticationHandler: func(ctx Context, r TerminalAuthenticationRequest) (TerminalExitStatus, error) {
		logins.Add(1)
		if r.Command != "host-owned-agent" || r.CWD != "/workspace" || r.Env["HOME"] != "/fixture/home" || r.Env["ACP_INTERACTIVE_LOGIN"] != "1" {
			return TerminalExitStatus{}, fmt.Errorf("bad invocation: %+v", r)
		}
		zero := uint32(0)
		return TerminalExitStatus{ExitCode: &zero}, nil
	}}
	factory := func(ctx context.Context, input ConnectionFactoryInput) (ConnectionHandle, error) {
		mode := "normal"
		if starts.Add(1) == 1 {
			mode = "auth-first"
		}
		return startIndependentAgent(t, ctx, input.Client, mode), nil
	}
	service := NewSessionService(factory, options)
	driver, err := service.Create(ctx, StartSessionOptions{Agent: Agent{Command: "host-owned-agent", Env: map[string]string{"HOME": "/fixture/home"}}, CWD: "/workspace"})
	if err != nil {
		t.Fatal(err)
	}
	defer driver.Close(context.Background())
	if starts.Load() != 2 || logins.Load() != 1 {
		t.Fatalf("starts=%d logins=%d", starts.Load(), logins.Load())
	}
	// No terminal command was executed and the independent peer aborts on any
	// authenticate request. Session creation after reconnect verifies the path.
}
func TestStableSchemaValidatesActualEncodedResponses(t *testing.T) {
	python := requireSchemaPython(t)
	cases := []struct {
		definition string
		value      any
	}{
		{"RequestPermissionResponse", PermissionDecision{Outcome: "selected", OptionID: "approve-one"}},
		{"RequestPermissionResponse", PermissionDecision{Outcome: "cancelled"}},
		{"SessionConfigOption", SessionConfigOption{Type: "boolean", ID: "enabled", Name: "Enabled", Value: false}},
		{"SetSessionConfigOptionRequest", SetSessionConfigOptionRequest{SessionID: "s", OptionID: "enabled", Value: false}},
		{"CreateElicitationResponse", ElicitationResponse{Action: "decline"}},
		{"CreateElicitationResponse", ElicitationResponse{Action: "cancel"}},
		{"SessionConfigOption", SessionConfigOption{Type: "select", ID: "model", Name: "Model", Value: "one", Groups: []SessionConfigGroup{{ID: "group-a", Name: "Models", Options: []SessionConfigChoice{{Value: "one", Name: "One"}}}}}},
		{"SessionNotification", SessionNotification{SessionID: "s", Update: SessionUpdate{SessionUpdate: "agent_message_chunk", Text: "hello"}}},
		{"SessionNotification", SessionNotification{SessionID: "s", Update: SessionUpdate{SessionUpdate: "agent_message_chunk", Text: ""}}},
	}
	for _, tc := range cases {
		t.Run(tc.definition, func(t *testing.T) {
			data, err := json.Marshal(tc.value)
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(python, "-c", `import json,sys;from jsonschema import Draft202012Validator;s=json.load(open('testdata/acp/schema-v1.23.0/schema.json'));Draft202012Validator({'$ref':'#/$defs/'+sys.argv[1],'$defs':s['$defs']}).validate(json.load(sys.stdin))`, tc.definition)
			cmd.Stdin = bytes.NewReader(data)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("schema rejected actual output %s: %s %v", data, out, err)
			}
		})
	}
}
