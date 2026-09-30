package acpruntime

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestStableTerminalAuthenticationInvocation(t *testing.T) {
	agent := Agent{Command: "configured-agent", Args: []string{"--base"}, ExtraArgs: []string{"--extra"}, Env: map[string]string{"HOME": "/home/fixture", "BASE": "old"}}
	method := RuntimeAuthenticationMethod{ID: "login", Name: "Login", Type: "terminal", Args: []string{"--login"}, Env: map[string]string{"BASE": "override", "LOGIN": "1"}}
	got, err := terminalAuthenticationInvocation(agent, "/work", method)
	if err != nil {
		t.Fatal(err)
	}
	if got.Command != agent.Command || got.CWD != "/work" || !reflect.DeepEqual(got.Args, []string{"--base", "--extra", "--login"}) || got.Env["HOME"] != "/home/fixture" || got.Env["BASE"] != "override" {
		t.Fatalf("bad invocation %+v", got)
	}
	got.Args[0] = "mutated"
	got.Env["HOME"] = "mutated"
	if agent.Args[0] != "--base" || agent.Env["HOME"] != "/home/fixture" {
		t.Fatal("auth invocation aliases host config")
	}
}
func TestStableTerminalMethodNeverAuthenticate(t *testing.T) {
	c := stableConnection(t, Client{})
	c.initialized = true
	c.authMethods = map[string]string{"terminal-login": "terminal"}
	if _, err := c.Authenticate(context.Background(), AuthenticateRequest{MethodID: "terminal-login"}); err == nil || !strings.Contains(err.Error(), "cannot be sent") {
		t.Fatalf("error=%v", err)
	}
	if len(c.peer.writer.(*syncBuffer).Bytes()) != 0 {
		t.Fatal("terminal ID sent on wire")
	}
}
func TestStableTerminalAuthFailureDoesNotReconnect(t *testing.T) {
	one, zero := uint32(1), uint32(0)
	signal := "TERM"
	for _, tc := range []struct {
		name    string
		handler TerminalAuthenticationHandler
	}{
		{"no handler", nil},
		{"nonzero", func(Context, TerminalAuthenticationRequest) (TerminalExitStatus, error) {
			return TerminalExitStatus{ExitCode: &one}, nil
		}},
		{"signal", func(Context, TerminalAuthenticationRequest) (TerminalExitStatus, error) {
			return TerminalExitStatus{Signal: &signal}, nil
		}},
		{"zero with signal", func(Context, TerminalAuthenticationRequest) (TerminalExitStatus, error) {
			return TerminalExitStatus{ExitCode: &zero, Signal: &signal}, nil
		}},
		{"no exit", func(Context, TerminalAuthenticationRequest) (TerminalExitStatus, error) {
			return TerminalExitStatus{}, nil
		}},
		{"handler error", func(Context, TerminalAuthenticationRequest) (TerminalExitStatus, error) {
			return TerminalExitStatus{}, errors.New("launch failed")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := stableConnection(t, Client{})
			input := ConnectionFactoryInput{Agent: Agent{Command: "configured-agent"}}
			resp := InitializeResponse{ProtocolVersion: 1, AuthMethods: []AuthMethod{{Type: "terminal", ID: "login", Name: "Login"}}}
			factory := func(context.Context, ConnectionFactoryInput) (ConnectionHandle, error) {
				t.Fatal("reconnected after failed auth")
				return ConnectionHandle{}, nil
			}
			_, _, err := authenticateBootstrap(context.Background(), RuntimeOptions{TerminalAuthenticationHandler: tc.handler}, factory, input, ConnectionHandle{Connection: c}, resp, ResolveAgentProfile(input.Agent))
			if err == nil {
				t.Fatal("failed terminal auth succeeded")
			}
			if len(c.peer.writer.(*syncBuffer).Bytes()) != 0 {
				t.Fatal("failed terminal auth emitted authenticate")
			}
		})
	}
}
func TestStableTerminalAuthCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	c := stableConnection(t, Client{})
	input := ConnectionFactoryInput{Agent: Agent{Command: "configured-agent"}}
	options := RuntimeOptions{TerminalAuthenticationHandler: func(Context, TerminalAuthenticationRequest) (TerminalExitStatus, error) {
		cancel()
		zero := uint32(0)
		return TerminalExitStatus{ExitCode: &zero}, nil
	}}
	_, _, err := authenticateBootstrap(ctx, options, nil, input, ConnectionHandle{Connection: c}, InitializeResponse{AuthMethods: []AuthMethod{{ID: "login", Name: "Login", Type: "terminal"}}}, ResolveAgentProfile(input.Agent))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled auth error=%v", err)
	}
}
