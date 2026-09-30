package acpruntime

import (
	"context"
	"fmt"
)

// TerminalAuthenticationHandler performs an explicitly selected interactive
// login using the supplied host-owned invocation. It must honor cancellation,
// expose the interactive terminal to the user, and never capture credentials.
// Returning zero exit status is followed by a fresh ACP connection; it does not
// by itself prove that authentication-gated agent operations will succeed.
type TerminalAuthenticationHandler func(Context, TerminalAuthenticationRequest) (TerminalExitStatus, error)
type TerminalAuthenticationRequest struct {
	MethodID string
	Name     string
	Command  string
	Args     []string
	Env      map[string]string
	CWD      string
}

func authenticateBootstrap(ctx context.Context, options RuntimeOptions, factory ConnectionFactory, input ConnectionFactoryInput, handle ConnectionHandle, resp InitializeResponse, profile AgentProfile) (ConnectionHandle, InitializeResponse, error) {
	methods := profile.NormalizeInitializeAuthMethods(input.Agent, resp.AuthMethods)
	runtimeMethods := profile.NormalizeRuntimeAuthMethods(input.Agent, runtimeAuthMethodsFromACP(methods))
	if len(runtimeMethods) == 0 {
		return handle, resp, nil
	}
	method, ok := selectRuntimeAuthenticationMethod(runtimeMethods)
	if options.AuthenticationHandler != nil {
		decision, err := options.AuthenticationHandler(ctx, runtimeMethods)
		if err != nil {
			return handle, resp, err
		}
		if decision.MethodID == "" {
			return handle, resp, nil
		}
		ok = false
		for _, candidate := range runtimeMethods {
			if candidate.ID == decision.MethodID {
				method = candidate
				ok = true
				break
			}
		}
		if !ok {
			return handle, resp, wrapError(ErrorAuthentication, "authenticate", "authentication method was not advertised", nil)
		}
	}
	if !ok {
		return handle, resp, nil
	}
	switch method.Type {
	case "", "agent":
		_, err := handle.Connection.Authenticate(ctx, AuthenticateRequest{MethodID: method.ID})
		if err != nil && !isAuthenticationNotImplemented(err) {
			return handle, resp, wrapError(ErrorAuthentication, "authenticate", "agent authentication failed", err)
		}
		return handle, resp, nil
	case "terminal":
		if options.TerminalAuthenticationHandler == nil {
			return handle, resp, wrapError(ErrorAuthentication, "terminal-auth", "terminal authentication handler is required", nil)
		}
		invocation, err := terminalAuthenticationInvocation(input.Agent, input.CWD, method)
		if err != nil {
			return handle, resp, err
		}
		exit, err := options.TerminalAuthenticationHandler(ctx, invocation)
		if ctx.Err() != nil {
			return handle, resp, ctx.Err()
		}
		if err != nil {
			return handle, resp, wrapError(ErrorAuthentication, "terminal-auth", "terminal authentication failed", err)
		}
		if exit.ExitCode == nil || *exit.ExitCode != 0 || exit.Signal != nil {
			return handle, resp, wrapError(ErrorAuthentication, "terminal-auth", "terminal authentication did not exit successfully", nil)
		}
		// Dispose the stale connection before constructing a new one. There is one
		// terminal attempt only; the first gated session operation verifies login.
		if err := runSessionCleanup(handle.Dispose); err != nil {
			return handle, resp, wrapError(ErrorAuthentication, "terminal-auth", "cannot dispose pre-authentication connection", err)
		}
		next, err := factory(ctx, input)
		if err != nil {
			return ConnectionHandle{}, InitializeResponse{}, wrapError(ErrorAuthentication, "terminal-auth", "authentication reconnect failed", err)
		}
		fresh, err := next.Connection.Initialize(ctx, InitializeRequest{ProtocolVersion: ProtocolVersion, ClientInfo: &input.Client.Info, ClientCapabilities: input.Client.Capabilities})
		if err != nil {
			return next, fresh, wrapError(ErrorAuthentication, "terminal-auth", "authentication reinitialize failed", err)
		}
		if fresh.ProtocolVersion != ProtocolVersion {
			return next, fresh, wrapError(ErrorProtocol, "initialize", "authentication reconnect did not negotiate protocol 1", nil)
		}
		return next, fresh, nil
	default:
		return handle, resp, wrapError(ErrorAuthentication, "authenticate", "unsupported authentication method type", nil)
	}
}
func terminalAuthenticationInvocation(agent Agent, cwd string, method RuntimeAuthenticationMethod) (TerminalAuthenticationRequest, error) {
	if agent.Command == "" {
		return TerminalAuthenticationRequest{}, fmt.Errorf("terminal authentication requires a configured agent command")
	}
	args := append([]string(nil), agent.Args...)
	args = append(args, agent.ExtraArgs...)
	args = append(args, method.Args...)
	env := make(map[string]string, len(agent.Env)+len(method.Env))
	for k, v := range agent.Env {
		env[k] = v
	}
	for k, v := range method.Env {
		env[k] = v
	}
	return TerminalAuthenticationRequest{MethodID: method.ID, Name: method.Name, Command: agent.Command, Args: args, Env: env, CWD: cwd}, nil
}
func terminalAuthenticationCapabilities(handler TerminalAuthenticationHandler) *AuthCapabilities {
	if handler == nil {
		return nil
	}
	return &AuthCapabilities{Terminal: true}
}
