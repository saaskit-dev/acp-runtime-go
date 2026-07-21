package acpruntime

import (
	"errors"
	"strings"
	"testing"
)

func TestRuntimeErrorErrorIncludesSessionAndCleanup(t *testing.T) {
	err := &RuntimeError{
		Kind:          ErrorInitialConfig,
		Op:            "session.initial_config",
		Msg:           "failed to apply initial config",
		Cause:         errors.New("unknown mode"),
		SessionID:     "sess-1",
		CleanupStatus: CleanupSucceeded,
	}
	got := err.Error()
	for _, want := range []string{
		"session.initial_config",
		"failed to apply initial config",
		"unknown mode",
		"session_id=sess-1",
		"cleanup=succeeded",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("Error() = %q, want substring %q", got, want)
		}
	}
}

func TestRuntimeErrorErrorIncludesCleanupError(t *testing.T) {
	err := &RuntimeError{
		Kind:          ErrorInitialConfig,
		Op:            "session.initial_config",
		Msg:           "failed",
		SessionID:     "sess-2",
		CleanupStatus: CleanupFailed,
		CleanupError:  errors.New("delete refused"),
	}
	got := err.Error()
	if !strings.Contains(got, "cleanup_error=delete refused") {
		t.Fatalf("Error() = %q, want cleanup_error", got)
	}
}
