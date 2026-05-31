package acpruntime

import (
	"context"
	"sync"
)

type Session struct {
	runtime *Runtime
	driver  SessionDriver

	mu     sync.RWMutex
	closed bool
}

func (s *Session) Capabilities() RuntimeCapabilities {
	return s.driver.Capabilities()
}

func (s *Session) Diagnostics() RuntimeDiagnostics {
	return s.driver.Diagnostics()
}

func (s *Session) Metadata() RuntimeSessionMetadata {
	return s.driver.Metadata()
}

func (s *Session) Status() string {
	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return "closed"
	}
	s.mu.RUnlock()
	return s.driver.Status()
}

func (s *Session) Snapshot() RuntimeSnapshot {
	return s.driver.Snapshot()
}

func (s *Session) Close(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	if s.runtime != nil {
		s.runtime.unregister(s.driver)
	}
	return s.driver.Close(ctx)
}

func (s *Session) StartTurn(ctx context.Context, prompt RuntimePrompt) TurnHandle {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return closedTurnHandle(sessionClosedError("session.start_turn"))
	}
	return s.driver.StartTurn(ctx, prompt)
}

func (s *Session) Run(ctx context.Context, text string) (TurnCompletion, error) {
	handle := s.StartTurn(ctx, RuntimePrompt{Text: text})
	result := <-handle.Completion
	return result.Completion, result.Err
}

func (s *Session) CancelTurn(ctx context.Context, turnID string) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return false, sessionClosedError("session.cancel_turn")
	}
	return s.driver.CancelTurn(ctx, turnID)
}

func (s *Session) SetAgentMode(ctx context.Context, modeID string) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return sessionClosedError("session.set_agent_mode")
	}
	return s.driver.SetAgentMode(ctx, modeID)
}

func (s *Session) SetAgentConfigOption(ctx context.Context, id string, value any) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return sessionClosedError("session.set_agent_config_option")
	}
	return s.driver.SetAgentConfigOption(ctx, id, value)
}

func (s *Session) ThreadEntries() []ThreadEntry {
	return s.driver.ThreadEntries()
}

func (s *Session) ToolCalls() []ToolCallSnapshot {
	return s.driver.ToolCalls()
}

func (s *Session) Operations() []Operation {
	return s.driver.Operations()
}

func (s *Session) PermissionRequests() []PermissionRequestSnapshot {
	return s.driver.PermissionRequests()
}

func sessionClosedError(op string) error {
	return &RuntimeError{Kind: ErrorSessionClosed, Op: op, Msg: "session is closed"}
}

func closedTurnHandle(err error) TurnHandle {
	events := make(chan TurnEvent)
	close(events)
	completion := make(chan TurnResult, 1)
	completion <- TurnResult{Err: err}
	close(completion)
	return TurnHandle{Events: events, Completion: completion}
}
