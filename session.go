package acpruntime

import "context"

type Session struct {
	runtime *Runtime
	driver  SessionDriver
	closed  bool
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
	if s.closed {
		return "closed"
	}
	return s.driver.Status()
}

func (s *Session) Snapshot() RuntimeSnapshot {
	return s.driver.Snapshot()
}

func (s *Session) Close(ctx context.Context) error {
	if s.closed {
		return nil
	}
	s.closed = true
	if s.runtime != nil {
		s.runtime.unregister(s.driver)
	}
	return s.driver.Close(ctx)
}

func (s *Session) StartTurn(ctx context.Context, prompt RuntimePrompt) TurnHandle {
	return s.driver.StartTurn(ctx, prompt)
}

func (s *Session) Run(ctx context.Context, text string) (TurnCompletion, error) {
	handle := s.StartTurn(ctx, RuntimePrompt{Text: text})
	result := <-handle.Completion
	return result.Completion, result.Err
}

func (s *Session) CancelTurn(ctx context.Context, turnID string) (bool, error) {
	return s.driver.CancelTurn(ctx, turnID)
}

func (s *Session) SetAgentMode(ctx context.Context, modeID string) error {
	return s.driver.SetAgentMode(ctx, modeID)
}

func (s *Session) SetAgentConfigOption(ctx context.Context, id string, value any) error {
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
