package acpruntime

import (
	"context"
	"os"
	"sync"
)

type Runtime struct {
	options RuntimeOptions
	service *SessionService

	mu       sync.Mutex
	sessions map[string]*managedSession
}

type managedSession struct {
	driver SessionDriver
	refs   int
}

func NewRuntime(factory ConnectionFactory, options RuntimeOptions) *Runtime {
	if factory == nil {
		factory = NewStdioConnectionFactory(StdioFactoryOptions{})
	}
	return &Runtime{
		options:  options,
		service:  NewSessionService(factory, options),
		sessions: map[string]*managedSession{},
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
	return r.register(driver), nil
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
	return r.register(driver), nil
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
	return r.register(driver), nil
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
	return r.register(driver), nil
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
	r.sessions = map[string]*managedSession{}
	r.mu.Unlock()
	var firstErr error
	for _, driver := range sessions {
		if err := driver.Close(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (r *Runtime) register(driver SessionDriver) *Session {
	snapshot := driver.Snapshot()
	r.mu.Lock()
	r.sessions[snapshot.Session.ID] = &managedSession{driver: driver, refs: 1}
	r.mu.Unlock()
	return &Session{runtime: r, driver: driver}
}

func (r *Runtime) unregister(driver SessionDriver) {
	id := driver.Snapshot().Session.ID
	r.mu.Lock()
	delete(r.sessions, id)
	r.mu.Unlock()
}

func (r *Runtime) resolveStartOptions(ctx context.Context, options StartSessionOptions) (StartSessionOptions, error) {
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
