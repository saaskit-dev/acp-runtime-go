package main

import (
	"os"
	"regexp"
	"strings"
)

var credentialDiagnostic = regexp.MustCompile(`(?i)(authorization[\s:=]+(?:bearer\s+)?|(?:api[_-]?key|access[_-]?token|password|secret)[\s"':=]+)[^\s,;"'}]+`)
var urlCredentials = regexp.MustCompile(`(https?://)[^\s/@:]+:[^\s/@]+@`)
var urlQueries = regexp.MustCompile(`(https?://[^\s?]+)\?[^\s]+`)

func redactDiagnostic(value string) string {
	// Match every sensitive environment value, but do not persist names or hashes.
	for _, entry := range os.Environ() {
		key, secret, ok := strings.Cut(entry, "=")
		if !ok || secret == "" {
			continue
		}
		upper := strings.ToUpper(key)
		if strings.Contains(upper, "KEY") || strings.Contains(upper, "TOKEN") || strings.Contains(upper, "SECRET") || strings.Contains(upper, "PASSWORD") {
			value = strings.ReplaceAll(value, secret, "[REDACTED]")
		}
	}
	value = credentialDiagnostic.ReplaceAllString(value, "${1}[REDACTED]")
	value = urlCredentials.ReplaceAllString(value, "${1}[REDACTED]@")
	value = urlQueries.ReplaceAllString(value, "${1}?[REDACTED]")
	if len(value) > 4096 {
		value = "[truncated] " + value[len(value)-4096:]
	}
	return value
}
