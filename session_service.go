package acpruntime

import (
	"context"
	"strings"
)

type SessionService struct {
	factory ConnectionFactory
	options RuntimeOptions
}

func NewSessionService(factory ConnectionFactory, options RuntimeOptions) *SessionService {
	return &SessionService{factory: factory, options: options}
}

func (s *SessionService) Create(ctx context.Context, input StartSessionOptions) (SessionDriver, error) {
	profile := ResolveAgentProfile(input.Agent)
	agent := input.Agent
	var sessionMeta map[string]any
	if input.SystemPrompt != nil {
		if profile.ApplySystemPromptToAgent != nil {
			agent = profile.ApplySystemPromptToAgent(agent, *input.SystemPrompt)
		}
		if profile.CreateSystemPromptSessionMeta != nil {
			sessionMeta = profile.CreateSystemPromptSessionMeta(*input.SystemPrompt)
		}
	}
	bootstrap, err := s.bootstrap(ctx, agent, input.CWD, input.MCPServers, input.Handlers, profile)
	if err != nil {
		return nil, wrapError(ErrorCreate, "session.start", "failed to bootstrap ACP session", err)
	}
	req := NewSessionRequest{CWD: input.CWD, MCPServers: normalizeMCPServers(input.MCPServers), AdditionalDirectories: input.AdditionalDirectories, Meta: sessionMeta}
	resp, err := bootstrap.Connection.NewSession(ctx, req)
	if err != nil {
		_ = bootstrap.Dispose(ctx)
		return nil, wrapError(ErrorCreate, "session.new", "failed to create ACP session", err)
	}
	bootstrap.SessionResponse = resp
	driver := newACPSessionDriver(bootstrap)
	if _, err := applyInitialConfig(ctx, driver, input.InitialConfig, profile); err != nil {
		_ = driver.Close(ctx)
		return nil, wrapError(ErrorInitialConfig, "session.initial_config", "failed to apply initial config", err)
	}
	return driver, nil
}

func (s *SessionService) Load(ctx context.Context, input LoadSessionOptions) (SessionDriver, error) {
	bootstrap, err := s.bootstrap(ctx, input.Agent, input.CWD, input.MCPServers, input.Handlers, ResolveAgentProfile(input.Agent))
	if err != nil {
		return nil, wrapError(ErrorLoad, "session.load", "failed to bootstrap ACP session", err)
	}
	resp, err := bootstrap.Connection.LoadSession(ctx, LoadSessionRequest{SessionID: input.SessionID, CWD: input.CWD, MCPServers: normalizeMCPServers(input.MCPServers), AdditionalDirectories: input.AdditionalDirectories})
	if err != nil {
		_ = bootstrap.Dispose(ctx)
		return nil, wrapError(ErrorLoad, "session.load", "failed to load ACP session", err)
	}
	bootstrap.SessionResponse = resp
	return newACPSessionDriver(bootstrap), nil
}

func (s *SessionService) Resume(ctx context.Context, input ResumeSessionOptions) (SessionDriver, error) {
	bootstrap, err := s.bootstrap(ctx, input.Agent, input.CWD, input.MCPServers, input.Handlers, ResolveAgentProfile(input.Agent))
	if err != nil {
		return nil, wrapError(ErrorResume, "session.resume", "failed to bootstrap ACP session", err)
	}
	resp, err := bootstrap.Connection.ResumeSession(ctx, ResumeSessionRequest{SessionID: input.SessionID, CWD: input.CWD, MCPServers: normalizeMCPServers(input.MCPServers), AdditionalDirectories: input.AdditionalDirectories})
	if err != nil {
		_ = bootstrap.Dispose(ctx)
		return nil, wrapError(ErrorResume, "session.resume", "failed to resume ACP session", err)
	}
	bootstrap.SessionResponse = resp
	driver := newACPSessionDriver(bootstrap)
	if _, err := applyInitialConfig(ctx, driver, input.InitialConfig, bootstrap.Profile); err != nil {
		_ = driver.Close(ctx)
		return nil, wrapError(ErrorInitialConfig, "session.initial_config", "failed to apply initial config", err)
	}
	return driver, nil
}

func (s *SessionService) Fork(ctx context.Context, input ForkSessionOptions) (SessionDriver, error) {
	bootstrap, err := s.bootstrap(ctx, input.Agent, input.CWD, input.MCPServers, input.Handlers, ResolveAgentProfile(input.Agent))
	if err != nil {
		return nil, wrapError(ErrorFork, "session.fork", "failed to bootstrap ACP session", err)
	}
	resp, err := bootstrap.Connection.ForkSession(ctx, ForkSessionRequest{SessionID: input.SessionID, CWD: input.CWD, MCPServers: normalizeMCPServers(input.MCPServers), AdditionalDirectories: input.AdditionalDirectories})
	if err != nil {
		_ = bootstrap.Dispose(ctx)
		return nil, wrapError(ErrorFork, "session.fork", "failed to fork ACP session", err)
	}
	bootstrap.SessionResponse = resp
	return newACPSessionDriver(bootstrap), nil
}

func (s *SessionService) ListAgentSessions(ctx context.Context, input ListSessionsOptions) (RuntimeSessionList, error) {
	profile := ResolveAgentProfile(input.Agent)
	bootstrap, err := s.bootstrap(ctx, input.Agent, input.CWD, nil, input.Handlers, profile)
	if err != nil {
		return RuntimeSessionList{}, wrapError(ErrorList, "session.list", "failed to bootstrap ACP session", err)
	}
	defer bootstrap.Dispose(ctx)
	resp, err := bootstrap.Connection.ListSessions(ctx, ListSessionsRequest{CWD: input.CWD, Cursor: input.Cursor, AdditionalDirectories: input.AdditionalDirectories})
	if err != nil {
		return RuntimeSessionList{}, wrapError(ErrorList, "session.list", "failed to list ACP sessions", err)
	}
	out := RuntimeSessionList{NextCursor: resp.NextCursor}
	for _, session := range resp.Sessions {
		ref := RuntimeSessionReference{ID: session.SessionID, AgentType: input.Agent.Type, CWD: session.CWD, Source: "remote"}
		if session.Title != nil {
			ref.Title = *session.Title
		}
		if session.UpdatedAt != nil {
			ref.UpdatedAt = *session.UpdatedAt
		}
		out.Sessions = append(out.Sessions, ref)
	}
	return out, nil
}

func (s *SessionService) bootstrap(ctx context.Context, agent Agent, cwd string, mcp []MCPServer, handlers AuthorityHandlers, profile AgentProfile) (sessionBootstrap, error) {
	client := defaultClient(s.options, handlers)
	handle, err := s.factory(ctx, ConnectionFactoryInput{Agent: agent, Client: client, CWD: cwd, Observability: s.options.Observability, Authority: client.Authority})
	if err != nil {
		return sessionBootstrap{}, err
	}
	resp, err := handle.Connection.Initialize(ctx, InitializeRequest{ProtocolVersion: ProtocolVersion, ClientInfo: &client.Info, ClientCapabilities: client.Capabilities})
	if err != nil {
		_ = handle.Dispose(ctx)
		return sessionBootstrap{}, err
	}
	methods := profile.NormalizeInitializeAuthMethods(agent, resp.AuthMethods)
	runtimeMethods := profile.NormalizeRuntimeAuthMethods(agent, runtimeAuthMethodsFromACP(methods))
	if len(runtimeMethods) > 0 {
		method, ok := selectRuntimeAuthenticationMethod(runtimeMethods)
		if ok && (method.Type == "agent" || method.Type == "") && s.options.AuthenticationHandler == nil {
			_, err := handle.Connection.Authenticate(ctx, AuthenticateRequest{MethodID: method.ID})
			if err != nil && !isAuthenticationNotImplemented(err) {
				_ = handle.Dispose(ctx)
				return sessionBootstrap{}, wrapError(ErrorAuthentication, "authenticate", "agent authentication failed", err)
			}
		} else if s.options.AuthenticationHandler != nil {
			decision, err := s.options.AuthenticationHandler(ctx, runtimeMethods)
			if err != nil {
				_ = handle.Dispose(ctx)
				return sessionBootstrap{}, err
			}
			if decision.MethodID != "" {
				_, err = handle.Connection.Authenticate(ctx, AuthenticateRequest{MethodID: decision.MethodID})
				if err != nil && !isAuthenticationNotImplemented(err) {
					_ = handle.Dispose(ctx)
					return sessionBootstrap{}, wrapError(ErrorAuthentication, "authenticate", "agent authentication failed", err)
				}
			}
		}
	}
	return sessionBootstrap{Agent: agent, CWD: cwd, MCPServers: mcp, Connection: handle.Connection, Dispose: handle.Dispose, InitializeResponse: resp, Profile: profile}, nil
}

func isAuthenticationNotImplemented(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "authentication not implemented")
}

func normalizeMCPServers(servers []MCPServer) []MCPServer {
	if servers == nil {
		return []MCPServer{}
	}
	return servers
}

func applyInitialConfig(ctx context.Context, driver *acpSessionDriver, config InitialConfig, profile AgentProfile) (InitialConfigReport, error) {
	var report InitialConfigReport
	if config.Mode != nil {
		mode, ok := config.Mode.(string)
		if ok {
			appliedMode, err := applyInitialConfigMode(ctx, driver, mode, profile)
			if err != nil {
				return report, err
			}
			report.Applied = append(report.Applied, InitialConfigReportItem{Key: "mode", ID: "mode", Value: appliedMode})
		}
	}
	if config.Model != nil {
		item, err := applyInitialConfigOption(ctx, driver, profile, "model", config.Model)
		if err != nil {
			return report, err
		}
		report.Applied = append(report.Applied, item)
	}
	if config.Effort != nil {
		item, err := applyInitialConfigOption(ctx, driver, profile, "effort", config.Effort)
		if err != nil {
			return report, err
		}
		report.Applied = append(report.Applied, item)
	}
	for id, value := range config.Raw {
		if err := driver.SetAgentConfigOption(ctx, id, value); err != nil {
			return report, err
		}
		report.Applied = append(report.Applied, InitialConfigReportItem{Key: id, ID: id, Value: value})
	}
	return report, nil
}

func applyInitialConfigOption(ctx context.Context, driver *acpSessionDriver, profile AgentProfile, key string, value any) (InitialConfigReportItem, error) {
	optionID := selectInitialConfigOption(driver.metadata.AgentConfigOptions, profile, key)
	if optionID == "" {
		return InitialConfigReportItem{Key: key, Value: value, Reason: "option_not_found"}, nil
	}
	var lastErr error
	for _, alias := range initialConfigAliases(profile, key, value) {
		if err := driver.SetAgentConfigOption(ctx, optionID, alias); err != nil {
			lastErr = err
			continue
		}
		return InitialConfigReportItem{Key: key, ID: optionID, Value: alias}, nil
	}
	if lastErr != nil {
		return InitialConfigReportItem{}, lastErr
	}
	return InitialConfigReportItem{Key: key, Value: value, Reason: "option_not_applied"}, nil
}

func selectInitialConfigOption(options []RuntimeAgentConfigOption, profile AgentProfile, key string) string {
	selector := InitialConfigOptionSelector{IDs: []string{key}}
	if profile.CreateInitialConfigOptionSelector != nil {
		selector = profile.CreateInitialConfigOptionSelector(key)
	}
	for _, option := range options {
		for _, id := range selector.IDs {
			if strings.EqualFold(option.ID, id) {
				return option.ID
			}
		}
		for _, category := range selector.Categories {
			if strings.EqualFold(option.Category, category) {
				return option.ID
			}
		}
	}
	return ""
}

func applyInitialConfigMode(ctx context.Context, driver *acpSessionDriver, mode string, profile AgentProfile) (string, error) {
	aliases := initialConfigAliases(profile, "mode", mode)
	var lastErr error
	for _, alias := range aliases {
		modeID, ok := alias.(string)
		if !ok || strings.TrimSpace(modeID) == "" {
			continue
		}
		if err := driver.SetAgentMode(ctx, modeID); err != nil {
			lastErr = err
			continue
		}
		return modeID, nil
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", driver.SetAgentMode(ctx, mode)
}

func initialConfigAliases(profile AgentProfile, key string, value any) []any {
	if profile.CreateInitialConfigAliases == nil {
		return []any{value}
	}
	aliases := profile.CreateInitialConfigAliases(key, value)
	if len(aliases) == 0 {
		return []any{value}
	}
	return aliases
}
