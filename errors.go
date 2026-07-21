package acpruntime

import "fmt"

// CleanupStatus describes durable provider-side session cleanup after a failed
// session operation. It does not describe process/connection teardown.
//
//   - CleanupSucceeded: the runtime deleted the durable session (e.g. Create
//     failed after session/new and session/delete completed).
//   - CleanupFailed: durable delete was attempted and failed; SessionID may still
//     exist on the provider.
//   - CleanupNotAttempted: durable delete was intentionally skipped (e.g. Resume
//     initial-config failure must not destroy an existing session).
type CleanupStatus string

const (
	CleanupNotAttempted CleanupStatus = "not_attempted"
	CleanupSucceeded    CleanupStatus = "succeeded"
	CleanupFailed       CleanupStatus = "failed"
)

type ErrorKind string

const (
	ErrorAuthentication ErrorKind = "authentication"
	ErrorCreate         ErrorKind = "create"
	ErrorFork           ErrorKind = "fork"
	ErrorInitialConfig  ErrorKind = "initial_config"
	ErrorLoad           ErrorKind = "load"
	ErrorList           ErrorKind = "list"
	ErrorPermission     ErrorKind = "permission"
	ErrorProcess        ErrorKind = "process"
	ErrorProtocol       ErrorKind = "protocol"
	ErrorResume         ErrorKind = "resume"
	ErrorSessionClosed  ErrorKind = "session_closed"
	ErrorSystemPrompt   ErrorKind = "system_prompt"
	ErrorTurnCancelled  ErrorKind = "turn_cancelled"
	ErrorTurnCoalesced  ErrorKind = "turn_coalesced"
	ErrorTurnTimeout    ErrorKind = "turn_timeout"
	ErrorTurnWithdrawn  ErrorKind = "turn_withdrawn"
)

type RuntimeError struct {
	Kind          ErrorKind
	Op            string
	Msg           string
	Cause         error
	SessionID     string
	CleanupStatus CleanupStatus
	CleanupError  error
}

func (e *RuntimeError) Error() string {
	if e == nil {
		return "<nil>"
	}
	var base string
	switch {
	case e.Op != "" && e.Msg != "" && e.Cause != nil:
		base = fmt.Sprintf("%s: %s: %v", e.Op, e.Msg, e.Cause)
	case e.Op != "" && e.Msg != "":
		base = fmt.Sprintf("%s: %s", e.Op, e.Msg)
	case e.Msg != "":
		base = e.Msg
	case e.Cause != nil:
		base = e.Cause.Error()
	default:
		base = string(e.Kind)
	}
	// Append structured fields that hosts often log only via Error(). SessionID
	// and cleanup outcome are operationally critical after failed Create/Resume.
	if e.SessionID != "" || e.CleanupStatus != "" {
		if e.SessionID != "" && e.CleanupStatus != "" {
			base = fmt.Sprintf("%s (session_id=%s cleanup=%s)", base, e.SessionID, e.CleanupStatus)
		} else if e.SessionID != "" {
			base = fmt.Sprintf("%s (session_id=%s)", base, e.SessionID)
		} else {
			base = fmt.Sprintf("%s (cleanup=%s)", base, e.CleanupStatus)
		}
	}
	if e.CleanupError != nil {
		base = fmt.Sprintf("%s (cleanup_error=%v)", base, e.CleanupError)
	}
	return base
}

func (e *RuntimeError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func wrapError(kind ErrorKind, op, msg string, err error) error {
	if err == nil {
		return &RuntimeError{Kind: kind, Op: op, Msg: msg}
	}
	if _, ok := err.(*RuntimeError); ok {
		return err
	}
	return &RuntimeError{Kind: kind, Op: op, Msg: msg, Cause: err}
}
