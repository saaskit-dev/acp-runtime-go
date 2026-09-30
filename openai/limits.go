package openai

import (
	"context"
	"errors"
	"net/http"

	acp "github.com/saaskit-dev/acp-runtime-go"
)

func serverClosedError() error {
	return sessionHTTPError{status: http.StatusServiceUnavailable, code: "server_closed", message: "ACP gateway is closing"}
}

// Reserve before creating any provider connection. Failed starts release their
// reservation; successful starts hold capacity until transport cleanup succeeds.
func (s *Server) startTrackedSession(ctx context.Context, runtime *acp.Runtime, options acp.StartSessionOptions) (*acp.Session, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, serverClosedError()
	}
	if s.maxConcurrentSessions > 0 && len(s.liveSessions)+s.pendingStarts >= s.maxConcurrentSessions {
		s.mu.Unlock()
		return nil, sessionHTTPError{status: http.StatusTooManyRequests, code: "session_limit", message: "maximum live sessions and in-flight starts reached"}
	}
	s.pendingStarts++
	s.mu.Unlock()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var stop func() bool
	if s.ctx != nil {
		stop = context.AfterFunc(s.ctx, cancel)
		defer stop()
	}
	session, err := runtime.StartSession(ctx, options)
	s.mu.Lock()
	s.pendingStarts--
	closed := s.closed
	if session != nil {
		if s.liveSessions == nil {
			s.liveSessions = make(map[*acp.Session]struct{})
		}
		s.liveSessions[session] = struct{}{}
	}
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if closed {
		_ = s.closeSession(context.Background(), session)
		return nil, serverClosedError()
	}
	return session, nil
}

func (s *Server) closeSession(ctx context.Context, session *acp.Session) error {
	if session == nil {
		return nil
	}
	if err := session.Close(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.liveSessions, session)
	s.mu.Unlock()
	return nil
}

func (s *Server) outputLimit() int {
	if s.maxOutputBytes <= 0 {
		return 8 * 1024 * 1024
	}
	return s.maxOutputBytes
}

func (s *Server) checkOutput(text string) error {
	if len(text) > s.outputLimit() {
		return sessionHTTPError{status: http.StatusBadGateway, code: "output_limit", message: "ACP output exceeded the gateway text byte limit"}
	}
	return nil
}

func isSessionError(err error, code string) bool {
	if err == nil {
		return false
	}
	return classifyError(err, "").code == code
}

// Use one classification for HTTP errors and failures after SSE headers commit.
func classifyError(err error, fallback string) sessionHTTPError {
	var value sessionHTTPError
	if errors.As(err, &value) {
		return value
	}
	var pointer *sessionHTTPError
	if errors.As(err, &pointer) {
		return *pointer
	}
	var runtimeErr *acp.RuntimeError
	if errors.As(err, &runtimeErr) {
		switch runtimeErr.Kind {
		case acp.ErrorTurnCoalesced:
			return sessionHTTPError{status: http.StatusConflict, code: "session_busy", message: err.Error()}
		case acp.ErrorSessionClosed:
			return sessionHTTPError{status: http.StatusConflict, code: "session_tainted", message: err.Error()}
		case acp.ErrorTurnTimeout:
			return sessionHTTPError{status: http.StatusGatewayTimeout, code: "acp_turn_timeout", message: err.Error()}
		}
	}
	return sessionHTTPError{status: http.StatusBadGateway, code: fallback, message: err.Error()}
}
