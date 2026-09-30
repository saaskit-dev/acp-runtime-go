package main

import (
	"context"
	"errors"
	"fmt"
	acp "github.com/saaskit-dev/acp-runtime-go"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// Match HTTP diagnostics, not arbitrary numbers in model output. In particular,
// JSON-RPC -32000 alone is not an infrastructure signal.
var infraHTTPStatus = regexp.MustCompile(`(?i)(?:unexpected status|http(?: status)?|status(?: code)?)[ :=]+(?:401|402|403|408|429|5[0-9]{2})\b`)

// classifyCheckResult covers both RPC errors and provider diagnostics returned
// as ordinary output. Unknown errors remain FAIL so protocol regressions are
// not silently suppressed. A successful sentinel takes precedence over
// transient diagnostic text from a recovered request.
func classifyCheckResult(err error, output string) string {
	if err == nil && hasSentinel(output) {
		return "PASS"
	}
	if isInfrastructureError(err, output) {
		return "INFRA_ERROR"
	}
	return "FAIL"
}

type failureKind string

const (
	failureInfra    failureKind = "infrastructure"
	failureProtocol failureKind = "protocol"
	failureProvider failureKind = "provider"
)

type checkError struct {
	Kind  failureKind
	Cause error
}

func (e *checkError) Error() string { return fmt.Sprintf("%s: %v", e.Kind, e.Cause) }
func (e *checkError) Unwrap() error { return e.Cause }
func hasSentinel(output string) bool {
	for _, line := range strings.Split(output, "\n") {
		if strings.TrimSpace(line) == sentinelToken {
			return true
		}
	}
	return false
}

func isInfrastructureError(err error, output string) bool {
	var typed *checkError
	if errors.As(err, &typed) {
		return typed.Kind == failureInfra
	}
	var rpc *acp.RPCError
	if errors.As(err, &rpc) && (rpc.Code == -32600 || rpc.Code == -32601 || rpc.Code == -32602 || rpc.Code == -32700) {
		return false
	}
	var runtimeErr *acp.RuntimeError
	if errors.As(err, &runtimeErr) {
		if runtimeErr.Kind == acp.ErrorProtocol {
			return false
		}
		if runtimeErr.Kind == acp.ErrorAuthentication {
			return true
		}
	}
	// Legacy provider diagnostics below are a fallback only after typed errors.

	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrPermission) ||
		errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	var networkError net.Error
	if errors.As(err, &networkError) {
		return true
	}
	message := output
	if err != nil {
		message += "\n" + err.Error()
	}
	message = strings.ToLower(message)
	if infraHTTPStatus.MatchString(message) {
		return true
	}
	for _, signal := range []string{
		"insufficient balance", "insufficient_quota", "credit balance is too low",
		"authentication_error", "invalid_api_key", "missing api key",
		"invalid api key", "not logged in", "not authenticated", "login required",
		"authentication required", "authentication failed", "oauth token has expired",
		"please login", "please log in", "rate_limit_exceeded",
		"executable file not found", "binary not found on path",
		"connection refused", "connection reset by peer", "no such host",
		"network is unreachable", "tls handshake timeout", "i/o timeout",
		"context deadline exceeded",
	} {
		if strings.Contains(message, signal) {
			return true
		}
	}
	return false
}

// FAIL wins over incomplete checks; only a fully verified run can close issues.
func resultExitCode(hasFailure, hasIncomplete bool) int {
	if hasFailure {
		return 1
	}
	if hasIncomplete {
		return 2
	}
	return 0
}
