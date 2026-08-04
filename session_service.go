package acpruntime

import (
	"context"
	"errors"
	"strings"
	"time"
)

const sessionCleanupTimeout = 5 * time.Second

func runSessionCleanup(cleanup func(context.Context) error) error {
	if cleanup == nil {
		return nil
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), sessionCleanupTimeout)
	defer cancel()
	return cleanup(cleanupCtx)
}

type SessionService struct {
	factory ConnectionFactory
	options RuntimeOptions
}

func NewSessionService(factory ConnectionFactory, options RuntimeOptions) *SessionService {
	return &SessionService{factory: factory, options: options}
}

func (s *SessionService) Create(ctx context.Context, input StartSessionOptions) (SessionDriver, error) {
	profile := ResolveAgentProfile(input.Agent)
	agent, sessionMeta, err := prepareAgentSessionStart(profile, input)
	if err != nil {
		return nil, err
	}
	bootstrap, err := s.bootstrap(ctx, agent, input.CWD, input.MCPServers, input.Handlers, profile)
	if err != nil {
		return nil, wrapError(ErrorCreate, "session.start", "failed to bootstrap ACP session", err)
	}
	bootstrap.QueuePolicy = resolveQueuePolicy(input.Queue)
	req := NewSessionRequest{CWD: input.CWD, MCPServers: normalizeMCPServers(input.MCPServers), AdditionalDirectories: input.AdditionalDirectories, Meta: sessionMeta}
	resp, err := bootstrap.Connection.NewSession(ctx, req)
	if err != nil {
		_ = runSessionCleanup(bootstrap.Dispose)
		return nil, wrapError(ErrorCreate, "session.new", "failed to create ACP session", err)
	}
	bootstrap.SessionResponse = resp
	driver := newACPSessionDriver(bootstrap)
	if _, err := applyInitialConfig(ctx, driver, input.InitialConfig, profile); err != nil {
		// session/new already created a durable provider session; delete it so a
		// failed initial config does not leave an orphan behind.
		cleanupErr := runSessionCleanup(driver.Delete)
		cleanupStatus := CleanupSucceeded
		if cleanupErr != nil {
			cleanupStatus = CleanupFailed
		}
		runtimeErr := &RuntimeError{
			Kind:          ErrorInitialConfig,
			Op:            "session.initial_config",
			Msg:           "failed to apply initial config",
			Cause:         err,
			SessionID:     resp.SessionID,
			CleanupStatus: cleanupStatus,
			CleanupError:  cleanupErr,
		}
		if s.options.Hooks.OnSessionEvent != nil {
			s.options.Hooks.OnSessionEvent(RuntimeSessionEvent{Type: "cleanup_failed", SessionID: resp.SessionID, AgentType: agent.Type, Err: runtimeErr})
		}
		return nil, runtimeErr
	}
	if s.options.Hooks.OnSessionEvent != nil {
		s.options.Hooks.OnSessionEvent(RuntimeSessionEvent{Type: "created", SessionID: resp.SessionID, AgentType: agent.Type})
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
		_ = runSessionCleanup(bootstrap.Dispose)
		return nil, wrapError(ErrorLoad, "session.load", "failed to load ACP session", err)
	}
	bootstrap.SessionResponse = resp
	return newACPSessionDriver(bootstrap), nil
}

func (s *SessionService) Resume(ctx context.Context, input ResumeSessionOptions) (SessionDriver, error) {
	profile := ResolveAgentProfile(input.Agent)
	// Resume uses the same logical prompt / AgentConfig / Meta projection path
	// as Create so hosts can reuse the same StartSessionOptions.
	agent, sessionMeta, err := prepareAgentSessionStart(profile, input.StartSessionOptions)
	if err != nil {
		return nil, err
	}
	bootstrap, err := s.bootstrap(ctx, agent, input.CWD, input.MCPServers, input.Handlers, profile)
	if err != nil {
		return nil, wrapError(ErrorResume, "session.resume", "failed to bootstrap ACP session", err)
	}
	bootstrap.QueuePolicy = resolveQueuePolicy(input.Queue)
	resp, err := bootstrap.Connection.ResumeSession(ctx, ResumeSessionRequest{
		SessionID:             input.SessionID,
		CWD:                   input.CWD,
		MCPServers:            normalizeMCPServers(input.MCPServers),
		AdditionalDirectories: input.AdditionalDirectories,
		Meta:                  sessionMeta,
	})
	if err != nil {
		_ = runSessionCleanup(bootstrap.Dispose)
		return nil, wrapError(ErrorResume, "session.resume", "failed to resume ACP session", err)
	}
	bootstrap.SessionResponse = resp
	driver := newACPSessionDriver(bootstrap)
	if _, err := applyInitialConfig(ctx, driver, input.InitialConfig, bootstrap.Profile); err != nil {
		// The durable provider session already existed before resume; only tear
		// down this connection. Report SessionID so hosts can retry or inspect
		// the existing session, and mark durable cleanup as not attempted.
		sessionID := resp.SessionID
		if sessionID == "" {
			sessionID = input.SessionID
		}
		cleanupErr := runSessionCleanup(driver.Close)
		return nil, &RuntimeError{
			Kind:          ErrorInitialConfig,
			Op:            "session.initial_config",
			Msg:           "failed to apply initial config",
			Cause:         err,
			SessionID:     sessionID,
			CleanupStatus: CleanupNotAttempted,
			CleanupError:  cleanupErr,
		}
	}
	if s.options.Hooks.OnSessionEvent != nil {
		s.options.Hooks.OnSessionEvent(RuntimeSessionEvent{Type: "resumed", SessionID: resp.SessionID, AgentType: agent.Type})
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
		_ = runSessionCleanup(bootstrap.Dispose)
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
	defer func() { _ = runSessionCleanup(bootstrap.Dispose) }()
	resp, err := bootstrap.Connection.ListSessions(ctx, ListSessionsRequest{CWD: input.CWD, Cursor: input.Cursor, AdditionalDirectories: input.AdditionalDirectories})
	if err != nil {
		return RuntimeSessionList{}, wrapError(ErrorList, "session.list", "failed to list ACP sessions", err)
	}
	// StoredSessionsEnabled tells the runtime whether the agent's sessions are
	// durably persisted (source "stored") or just an in-memory/remote view
	// (source "remote"). Hosts use this distinction to decide whether
	// session/load will succeed and whether to surface the sessions in a
	// history UI.
	source := "remote"
	if s.options.StoredSessionsEnabled {
		source = "stored"
	}
	out := RuntimeSessionList{NextCursor: resp.NextCursor}
	for _, session := range resp.Sessions {
		ref := RuntimeSessionReference{ID: session.SessionID, AgentType: input.Agent.Type, CWD: session.CWD, Source: source}
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
	agent = applyRuntimeEnvironment(agent, s.options)
	client := defaultClient(s.options, handlers)
	handle, err := s.factory(ctx, ConnectionFactoryInput{Agent: agent, Client: client, CWD: cwd, Observability: s.options.Observability, Authority: client.Authority})
	if err != nil {
		return sessionBootstrap{}, err
	}
	resp, err := handle.Connection.Initialize(ctx, InitializeRequest{ProtocolVersion: ProtocolVersion, ClientInfo: &client.Info, ClientCapabilities: client.Capabilities})
	if err != nil {
		_ = runSessionCleanup(handle.Dispose)
		return sessionBootstrap{}, err
	}
	methods := profile.NormalizeInitializeAuthMethods(agent, resp.AuthMethods)
	runtimeMethods := profile.NormalizeRuntimeAuthMethods(agent, runtimeAuthMethodsFromACP(methods))
	if len(runtimeMethods) > 0 {
		method, ok := selectRuntimeAuthenticationMethod(runtimeMethods)
		if ok && (method.Type == "agent" || method.Type == "") && s.options.AuthenticationHandler == nil {
			_, err := handle.Connection.Authenticate(ctx, AuthenticateRequest{MethodID: method.ID})
			if err != nil && !isAuthenticationNotImplemented(err) {
				_ = runSessionCleanup(handle.Dispose)
				return sessionBootstrap{}, wrapError(ErrorAuthentication, "authenticate", "agent authentication failed", err)
			}
		} else if s.options.AuthenticationHandler != nil {
			decision, err := s.options.AuthenticationHandler(ctx, runtimeMethods)
			if err != nil {
				_ = runSessionCleanup(handle.Dispose)
				return sessionBootstrap{}, err
			}
			if decision.MethodID != "" {
				_, err = handle.Connection.Authenticate(ctx, AuthenticateRequest{MethodID: decision.MethodID})
				if err != nil && !isAuthenticationNotImplemented(err) {
					_ = runSessionCleanup(handle.Dispose)
					return sessionBootstrap{}, wrapError(ErrorAuthentication, "authenticate", "agent authentication failed", err)
				}
			}
		}
	}
	return sessionBootstrap{
		Agent:              agent,
		CWD:                cwd,
		MCPServers:         mcp,
		Connection:         handle.Connection,
		Dispose:            handle.Dispose,
		InitializeResponse: resp,
		Profile:            profile,
		Hooks:              s.options.Hooks,
		ReadModelLimits:    s.options.ReadModelLimits,
	}, nil
}

func applyRuntimeEnvironment(agent Agent, options RuntimeOptions) Agent {
	if options.HomeDir == "" && options.CacheDir == "" {
		return agent
	}
	env := make(map[string]string, len(agent.Env)+3)
	for key, value := range agent.Env {
		env[key] = value
	}
	if options.HomeDir != "" {
		env["HOME"] = options.HomeDir
		env[RuntimeHomeDirEnvVar] = options.HomeDir
	}
	if options.CacheDir != "" {
		env[RuntimeCacheDirEnvVar] = options.CacheDir
	}
	agent.Env = env
	return agent
}

func isAuthenticationNotImplemented(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "authentication not implemented")
}

// isMethodNotImplemented reports whether err is a JSON-RPC -32601
// "method not found" response. Used to gracefully tolerate agents that have not
// implemented newer stable-v1 methods (e.g. logout, session/delete) so callers
// do not see hard failures when targeting older adapter versions.
func isMethodNotImplemented(err error) bool {
	if err == nil {
		return false
	}
	var rpcErr *RPCError
	if errors.As(err, &rpcErr) {
		return rpcErr.Code == -32601
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "method not found") || strings.Contains(msg, `"method not found"`)
}

func normalizeMCPServers(servers []MCPServer) []MCPServer {
	if servers == nil {
		return []MCPServer{}
	}
	return servers
}

// prepareAgentSessionStart extracts the public logical system-prompt keys,
// lets the provider profile project them exactly once, applies AgentConfig,
// then merges all remaining caller metadata. Create and Resume share this path.
func prepareAgentSessionStart(profile AgentProfile, input StartSessionOptions) (Agent, map[string]any, error) {
	agent := input.Agent
	prompt, callerMeta, err := extractSystemPrompt(input.Meta)
	if err != nil {
		return agent, nil, err
	}
	var sessionMeta map[string]any
	if prompt != nil {
		if profile.ProjectSystemPrompt == nil {
			return agent, nil, systemPromptError("agent profile does not support system prompt projection")
		}
		var promptMeta map[string]any
		agent, promptMeta = profile.ProjectSystemPrompt(agent, *prompt)
		sessionMeta = mergeSessionMeta(sessionMeta, promptMeta)
	}
	// Apply unified AgentConfig before remaining explicit Meta so caller keys
	// still win, except for the reserved prompt keys extracted above.
	if input.AgentConfig != nil && profile.ApplyAgentConfig != nil {
		var configMeta map[string]any
		agent, configMeta = profile.ApplyAgentConfig(agent, *input.AgentConfig)
		if hasSystemPromptKey(configMeta) {
			return agent, nil, systemPromptError("AgentConfig metadata contains a reserved system prompt key")
		}
		if len(configMeta) > 0 {
			sessionMeta = mergeSessionMeta(sessionMeta, configMeta)
		}
	}
	if len(callerMeta) > 0 {
		sessionMeta = mergeSessionMeta(sessionMeta, callerMeta)
	}
	return agent, sessionMeta, nil
}

func extractSystemPrompt(meta map[string]any) (*SystemPromptProjection, map[string]any, error) {
	remaining := make(map[string]any, len(meta))
	for key, value := range meta {
		if key != SystemPromptMetaKey && key != AppendSystemPromptMetaKey {
			remaining[key] = value
		}
	}
	replace, err := systemPromptText(meta, SystemPromptMetaKey)
	if err != nil {
		return nil, nil, err
	}
	appendText, err := systemPromptText(meta, AppendSystemPromptMetaKey)
	if err != nil {
		return nil, nil, err
	}
	if replace != "" && appendText != "" {
		return nil, nil, systemPromptError("_meta.systemPrompt and _meta.appendSystemPrompt are mutually exclusive")
	}
	if replace != "" {
		return &SystemPromptProjection{Mode: SystemPromptModeReplace, Text: replace}, remaining, nil
	}
	if appendText != "" {
		return &SystemPromptProjection{Mode: SystemPromptModeAppend, Text: appendText}, remaining, nil
	}
	return nil, remaining, nil
}

func systemPromptText(meta map[string]any, key string) (string, error) {
	value, ok := meta[key]
	if !ok {
		return "", nil
	}
	text, ok := value.(string)
	if !ok {
		return "", systemPromptError("_meta." + key + " must be a string")
	}
	if strings.TrimSpace(text) == "" {
		return "", nil
	}
	return text, nil
}

func hasSystemPromptKey(meta map[string]any) bool {
	_, replace := meta[SystemPromptMetaKey]
	_, appendPrompt := meta[AppendSystemPromptMetaKey]
	return replace || appendPrompt
}

func systemPromptError(message string) error {
	return &RuntimeError{Kind: ErrorSystemPrompt, Op: "session.system_prompt", Msg: message}
}

// mergeSessionMeta deep-merges two session _meta maps (session/new and
// session/resume). Values from extra take precedence over base at every level:
// for conflicting map keys, if both values are maps they are merged
// recursively, otherwise extra's value wins. A nil base is treated as empty.
// The returned map is newly allocated; neither input is mutated.
func mergeSessionMeta(base, extra map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(extra))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		if existing, ok := out[k]; ok {
			if existMap, ea := existing.(map[string]any); ea {
				if newMap, nb := v.(map[string]any); nb {
					out[k] = mergeSessionMeta(existMap, newMap)
					continue
				}
			}
		}
		out[k] = v
	}
	return out
}

// resolveQueuePolicy normalizes the host-supplied QueuePolicyInput into the
// internal QueuePolicy that governs turn-event delivery. The default ("buffer")
// streams every intermediate event; "drop" suppresses intermediate events so
// the consumer only observes the final completion. This is a runtime-level
// behavior, not an ACP wire field.
func resolveQueuePolicy(input QueuePolicyInput) QueuePolicy {
	delivery := strings.ToLower(strings.TrimSpace(input.Delivery))
	if delivery == "" {
		delivery = "buffer"
	}
	return QueuePolicy{Delivery: delivery}
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
