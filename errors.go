package acpruntime

import "fmt"

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
	Kind  ErrorKind
	Op    string
	Msg   string
	Cause error
}

func (e *RuntimeError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Op != "" && e.Msg != "" {
		if e.Cause != nil {
			return fmt.Sprintf("%s: %s: %v", e.Op, e.Msg, e.Cause)
		}
		return fmt.Sprintf("%s: %s", e.Op, e.Msg)
	}
	if e.Msg != "" {
		return e.Msg
	}
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return string(e.Kind)
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
