package acpruntime

import (
	"context"
	"fmt"
	"os"
	"sync"
)

type Runtime struct {
	options RuntimeOptions
	service *SessionService

	mu         sync.Mutex
	sessions   map[string]*managedSession
	nextHandle uint64
	closing    bool
}

type managedSession struct {
	driver SessionDriver
	refs   int
}

func NewRuntime(factory ConnectionFactory, options RuntimeOptions) *Runtime {
	if factory == nil {
		factory = NewStdioConnectionFactory(StdioFactoryOptions{})
	}
	// Native transports (Agent.Type claude-native / codex-native) route to
	// in-process bridges before the stdio ACP factory; everything else is
	// delegated unchanged, so the switch is invisible to existing hosts.
	factory = withNativeTransport(factory)
	return &Runtime{
		options:  options,
		service:  NewSessionService(factory, options),
		sessions: map[string]*managedSession{},
	}
}

// SetConnectionObserver installs a host decorator around every connection,
// including native loopback transports. Call before starting any sessions.
func (r *Runtime) SetConnectionObserver(observer func(context.Context, ConnectionFactoryInput, *ConnectionHandle)) {
	if observer == nil {
		return
	}
	base := r.service.factory
	r.service.factory = func(ctx context.Context, input ConnectionFactoryInput) (ConnectionHandle, error) {
		handle, err := base(ctx, input)
		if err == nil {
			observer(ctx, input, &handle)
		}
		return handle, err
	}
}

func (r *Runtime) StartSession(ctx context.Context, options StartSessionOptions) (*Session, error) {
	resolved, err := r.resolveStartOptions(ctx, options)
	if err != nil {
		return nil, err
	}
	driver, err := r.service.Create(ctx, resolved)
	if err != nil {
		return nil, err
	}
	return r.adopt(driver)
}

func (r *Runtime) LoadSession(ctx context.Context, options LoadSessionOptions) (*Session, error) {
	start, err := r.resolveStartOptions(ctx, options.StartSessionOptions)
	if err != nil {
		return nil, err
	}
	options.StartSessionOptions = start
	driver, err := r.service.Load(ctx, options)
	if err != nil {
		return nil, err
	}
	return r.adopt(driver)
}

func (r *Runtime) ResumeSession(ctx context.Context, options ResumeSessionOptions) (*Session, error) {
	start, err := r.resolveStartOptions(ctx, options.StartSessionOptions)
	if err != nil {
		return nil, err
	}
	options.StartSessionOptions = start
	driver, err := r.service.Resume(ctx, options)
	if err != nil {
		return nil, err
	}
	return r.adopt(driver)
}

func (r *Runtime) ForkSession(ctx context.Context, options ForkSessionOptions) (*Session, error) {
	start, err := r.resolveStartOptions(ctx, options.StartSessionOptions)
	if err != nil {
		return nil, err
	}
	options.StartSessionOptions = start
	driver, err := r.service.Fork(ctx, options)
	if err != nil {
		return nil, err
	}
	return r.adopt(driver)
}

func (r *Runtime) ListSessions(ctx context.Context, options ListSessionsOptions) (RuntimeSessionList, error) {
	if options.Agent.Command == "" {
		agent, err := ResolveRuntimeAgentFromRegistry(ctx, firstNonEmpty(options.AgentID, options.Agent.Type))
		if err != nil {
			return RuntimeSessionList{}, err
		}
		options.Agent = agent
	}
	if options.CWD == "" {
		cwd, _ := os.Getwd()
		options.CWD = cwd
	}
	return r.service.ListAgentSessions(ctx, options)
}

func (r *Runtime) Close(ctx context.Context) error {
	r.mu.Lock()
	sessions := make([]SessionDriver, 0, len(r.sessions))
	for _, entry := range r.sessions {
		sessions = append(sessions, entry.driver)
	}
	r.closing = true
	r.mu.Unlock()
	var firstErr error
	for _, driver := range sessions {
		if err := driver.Close(ctx); err != nil {
			if firstErr == nil {
				firstErr = err
			}
		} else {
			r.unregister(driver)
		}
	}
	if r.service != nil {
		if err := r.service.Close(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (r *Runtime) adopt(driver SessionDriver) (*Session, error) {
	session := r.register(driver)
	if session.Status() == "closed" {
		return nil, sessionClosedError("runtime.start_session")
	}
	return session, nil
}

func (r *Runtime) register(driver SessionDriver) *Session {
	r.mu.Lock()
	r.nextHandle++
	key := fmt.Sprintf("handle-%d", r.nextHandle)
	// Every transport retains separate ownership even if provider IDs repeat.
	r.sessions[key] = &managedSession{driver: driver, refs: 1}
	closing := r.closing
	r.mu.Unlock()
	session := newSession(r, driver)
	if closing {
		_ = session.Close(context.Background())
	}
	return session
}

func newSession(runtime *Runtime, driver SessionDriver) *Session {
	session := &Session{runtime: runtime, driver: driver}
	if source, ok := driver.(interface {
		SessionUpdates() <-chan SessionNotification
	}); ok {
		session.updates = source.SessionUpdates()
	}
	return session
}

func (r *Runtime) unregister(driver SessionDriver) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, entry := range r.sessions {
		if entry.driver == driver {
			delete(r.sessions, id)
		}
	}
}

func (r *Runtime) resolveStartOptions(ctx context.Context, options StartSessionOptions) (StartSessionOptions, error) {
	r.mu.Lock()
	closing := r.closing
	r.mu.Unlock()
	if closing {
		return options, sessionClosedError("runtime.start_session")
	}
	options = cloneStartOptions(options)
	if options.Agent.Command == "" {
		agentID := firstNonEmpty(options.AgentID, options.Agent.Type)
		if agentID == "" {
			agentID = LocalSimulatorAgentACPRegistryID
		}
		agent, err := ResolveRuntimeAgentFromRegistry(ctx, agentID)
		if err != nil {
			return options, err
		}
		options.Agent = agent
	}
	if options.Agent.Type == "" {
		options.Agent.Type = options.Agent.Command
	}
	if options.CWD == "" {
		cwd, _ := os.Getwd()
		options.CWD = cwd
	}
	return options, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
