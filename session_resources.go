package acpruntime

import (
	"context"
	"errors"
)

// connectionCleanup retains transport ownership before a public session exists,
// including failed initialize/new/resume and terminal-authentication reconnects.
type connectionCleanup struct {
	gate    contextLock
	owner   *SessionService
	id      uint64
	dispose func(context.Context) error
	done    bool
}

func (c *connectionCleanup) close(ctx context.Context) error {
	if err := c.gate.LockContext(ctx); err != nil {
		return err
	}
	defer c.gate.Unlock()
	if c.done {
		return nil
	}
	if err := c.dispose(ctx); err != nil {
		return err
	}
	c.done = true
	c.owner.cleanupMu.Lock()
	delete(c.owner.cleanups, c.id)
	c.owner.cleanupMu.Unlock()
	return nil
}
func (s *SessionService) openConnection(ctx context.Context, input ConnectionFactoryInput) (ConnectionHandle, error) {
	s.cleanupMu.Lock()
	closing := s.closing
	s.cleanupMu.Unlock()
	if closing {
		return ConnectionHandle{}, sessionClosedError("session.bootstrap")
	}
	handle, factoryErr := s.factory(ctx, input)
	if handle.Connection == nil && handle.Dispose == nil {
		return handle, factoryErr
	}
	dispose := handle.Dispose
	if dispose == nil {
		dispose = func(context.Context) error {
			if handle.Connection != nil {
				handle.Connection.peer.Close()
			}
			return nil
		}
	}
	s.cleanupMu.Lock()
	if s.cleanups == nil {
		s.cleanups = make(map[uint64]*connectionCleanup)
	}
	s.cleanupID++
	cleanup := &connectionCleanup{owner: s, id: s.cleanupID, dispose: dispose}
	s.cleanups[cleanup.id] = cleanup
	closing = s.closing
	s.cleanupMu.Unlock()
	handle.Dispose = cleanup.close
	if factoryErr != nil || closing || handle.Connection == nil {
		cause := factoryErr
		if cause == nil {
			cause = sessionClosedError("session.bootstrap")
		}
		return ConnectionHandle{}, cleanupAfterFailure(cause, cleanup.close)
	}
	if input.Client.runtimeManaged {
		handle.Connection.installStartupLeases()
	}
	return handle, nil
}

// Close stops admitting new transports and retries every outstanding cleanup,
// including failures which happened before a Session could be returned.
func (s *SessionService) Close(ctx context.Context) error {
	s.cleanupMu.Lock()
	s.closing = true
	pending := make([]*connectionCleanup, 0, len(s.cleanups))
	for _, cleanup := range s.cleanups {
		pending = append(pending, cleanup)
	}
	s.cleanupMu.Unlock()
	var result error
	for _, cleanup := range pending {
		result = errors.Join(result, cleanup.close(ctx))
	}
	return result
}

// Preserve the primary operation error while exposing failed cleanup separately.
func cleanupAfterFailure(cause error, dispose func(context.Context) error) error {
	cleanupErr := runSessionCleanup(dispose)
	if cleanupErr == nil {
		return cause
	}
	var existing *RuntimeError
	if errors.As(cause, &existing) {
		out := *existing
		out.CleanupError = errors.Join(out.CleanupError, cleanupErr)
		if out.CleanupStatus == "" {
			out.CleanupStatus = CleanupNotAttempted
		}
		return &out
	}
	return &RuntimeError{Kind: ErrorProcess, Op: "session.bootstrap", Msg: "operation failed with unresolved transport cleanup", Cause: cause, CleanupStatus: CleanupNotAttempted, CleanupError: cleanupErr}
}
