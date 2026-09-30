package harness

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"
)

type recorder struct {
	mu       sync.Mutex
	entries  []TranscriptEntry
	nextConn uint64
	sequence uint64
	changed  chan struct{}
}

func newRecorder() *recorder { return &recorder{changed: make(chan struct{}, 1)} }
func (r *recorder) connection() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextConn++
	return r.nextConn
}
func (r *recorder) observe(conn uint64, direction string, raw json.RawMessage) {
	var m struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	decodeErr := json.Unmarshal(raw, &m)
	var params struct {
		SessionID string `json:"sessionId"`
		Update    struct {
			Type string `json:"sessionUpdate"`
		} `json:"update"`
	}
	_ = json.Unmarshal(m.Params, &params)
	e := TranscriptEntry{ConnectionID: conn, Direction: direction, RequestID: m.ID, Method: m.Method, SessionID: params.SessionID, Params: m.Params, Response: m.Result, Error: m.Error, Raw: append(json.RawMessage(nil), raw...)}
	if decodeErr != nil {
		e.Malformed = string(raw)
		e.Raw = nil
	}
	switch direction {
	case "inbound":
		e.Phase = "received"
	case "outbound":
		e.Phase = "write-success"
	case "outbound_attempt":
		e.Direction = "outbound"
		e.Phase = "attempted"
	default:
		return
	}
	if e.Method == "session/update" {
		e.EventType = params.Update.Type
	}
	if e.Method == "session/request_permission" {
		e.EventType = "permission-request"
	}
	r.mu.Lock()
	if direction == "outbound" {
		// Preserve the attempt's observation order even if a fast peer responded
		// before the writer returned. Only a fully successful write promotes it.
		for i := len(r.entries) - 1; i >= 0; i-- {
			old := &r.entries[i]
			if old.ConnectionID == conn && old.Phase == "attempted" && string(old.Raw) == string(raw) {
				old.Phase = e.Phase
				r.mu.Unlock()
				r.signal()
				return
			}
		}
	}
	r.sequence++
	e.Sequence = r.sequence
	r.entries = append(r.entries, e)
	r.mu.Unlock()
	r.signal()
}
func (r *recorder) signal() {
	select {
	case r.changed <- struct{}{}:
	default:
	}
}
func (r *recorder) snapshot() []TranscriptEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	return resolveTranscript(append([]TranscriptEntry(nil), r.entries...))
}
func (r *recorder) position() uint64   { r.mu.Lock(); defer r.mu.Unlock(); return r.sequence }
func witnessed(e TranscriptEntry) bool { return e.Phase == "received" || e.Phase == "write-success" }
func resolveTranscript(entries []TranscriptEntry) []TranscriptEntry {
	for i := range entries {
		e := &entries[i]
		if e.Method != "" || len(e.RequestID) == 0 {
			continue
		}
		for _, req := range entries {
			if !witnessed(req) || req.Method == "" || len(req.Response) > 0 || len(req.Error) > 0 || len(req.RequestID) == 0 || req.ConnectionID != e.ConnectionID || req.Direction == e.Direction || string(req.RequestID) != string(e.RequestID) {
				continue
			}
			e.Method = req.Method
			e.SessionID = req.SessionID
			if e.SessionID == "" {
				var result struct {
					ID string `json:"sessionId"`
				}
				_ = json.Unmarshal(e.Response, &result)
				e.SessionID = result.ID
			}
			if e.Method == "session/prompt" && len(e.Response) > 0 && len(e.Error) == 0 {
				var response struct {
					StopReason string `json:"stopReason"`
				}
				_ = json.Unmarshal(e.Response, &response)
				if response.StopReason != "" {
					e.EventType = "completed"
					e.Terminal = true
				}
			}
			break
		}
	}
	return entries
}
func responses(entries []TranscriptEntry, method string) []TranscriptEntry {
	var out []TranscriptEntry
	for _, e := range resolveTranscript(entries) {
		if witnessed(e) && e.Method == method && len(e.Response) > 0 && len(e.Error) == 0 {
			out = append(out, e)
		}
	}
	return out
}
func hasMethod(entries []TranscriptEntry, method string) bool {
	if method == "" {
		return false
	}
	if len(responses(entries, method)) > 0 {
		return true
	}
	for _, e := range entries {
		if witnessed(e) && e.Method == method && len(e.RequestID) == 0 {
			return true
		}
	}
	return false
}
func hasEvent(entries []TranscriptEntry, event string) bool {
	if event == "" {
		return false
	}
	for _, e := range resolveTranscript(entries) {
		if witnessed(e) && e.EventType == event {
			return true
		}
	}
	return false
}
func field(raw json.RawMessage, path string) (any, bool) {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return nil, false
	}
	if path == "" {
		return value, true
	}
	for _, part := range strings.Split(path, ".") {
		switch v := value.(type) {
		case map[string]any:
			var ok bool
			value, ok = v[part]
			if !ok {
				return nil, false
			}
		case []any:
			i, err := strconv.Atoi(part)
			if err != nil || i < 0 || i >= len(v) {
				return nil, false
			}
			value = v[i]
		default:
			return nil, false
		}
	}
	return value, true
}
func matchesField(raw json.RawMessage, a Assertion) bool {
	value, ok := field(raw, a.Path)
	if !ok {
		return false
	}
	if a.NotEmpty {
		if value == nil {
			return false
		}
		v := reflect.ValueOf(value)
		switch v.Kind() {
		case reflect.String, reflect.Slice, reflect.Map:
			return v.Len() > 0
		}
		return true
	}
	expected, _ := json.Marshal(a.Equals)
	actual, _ := json.Marshal(value)
	return string(expected) == string(actual)
}
func validateAssertions(c Case, result Result) error {
	for _, e := range result.Transcript {
		if witnessed(e) && e.Malformed != "" {
			return fmt.Errorf("case %s: malformed JSON-RPC at connection %d sequence %d", c.ID, e.ConnectionID, e.Sequence)
		}
	}
	for _, a := range c.Assertions {
		if err := validateAssertion(result, a); err != nil {
			return fmt.Errorf("case %s: %w", c.ID, err)
		}
	}
	return nil
}
func validateAssertion(result Result, a Assertion) error {
	entries := resolveTranscript(result.Transcript)
	switch a.Type {
	case "transcript-has-method":
		if hasMethod(entries, a.Method) {
			return validatePermissionResponses(entries, a.Method)
		}
	case "transcript-has-event":
		if hasEvent(entries, a.EventType) {
			return nil
		}
	case "transcript-method-response-has":
		if a.Method == "" || a.Path == "" || (!a.NotEmpty && a.Equals == nil && !a.EqualsSet) {
			break
		}
		for _, e := range responses(entries, a.Method) {
			if matchesField(e.Response, a) {
				return nil
			}
		}
	case "transcript-event-field":
		if a.EventType == "" || a.Path == "" || (!a.NotEmpty && a.Equals == nil && !a.EqualsSet) {
			break
		}
		for _, e := range entries {
			if witnessed(e) && e.EventType == a.EventType && matchesField(e.Params, a) {
				return nil
			}
		}
	case "transcript-event-count":
		if a.EventType == "" || (a.Min == nil && a.Max == nil) || (a.Min != nil && *a.Min < 0) || (a.Max != nil && *a.Max < 0) || (a.Min != nil && a.Max != nil && *a.Min > *a.Max) {
			break
		}
		count := 0
		for _, e := range entries {
			if witnessed(e) && e.EventType == a.EventType {
				count++
			}
		}
		if (a.Min == nil || count >= *a.Min) && (a.Max == nil || count <= *a.Max) {
			return nil
		}
		return fmt.Errorf("event %s count=%d outside requested bounds", a.EventType, count)
	case "transcript-order":
		if a.First == "" || a.Then == "" {
			break
		}
		for _, first := range entries {
			if !witnessed(first) || first.Method != a.First || len(first.Params) == 0 || !entrySucceeded(entries, first) {
				continue
			}
			for _, then := range entries {
				if witnessed(then) && then.Method == a.Then && len(then.Params) > 0 && then.ConnectionID == first.ConnectionID && then.Sequence > first.Sequence && entrySucceeded(entries, then) {
					if a.Then == "session/cancel" {
						completed := false
						for _, terminal := range entries {
							if terminal.Terminal && terminal.ConnectionID == first.ConnectionID && string(terminal.RequestID) == string(first.RequestID) && terminal.Direction != first.Direction && terminal.Sequence < then.Sequence {
								completed = true
								break
							}
						}
						if completed {
							continue
						}
					}
					return nil
				}
			}
		}
	case "transcript-has-tool-update":
		for _, e := range entries {
			if !witnessed(e) || (e.EventType != "tool_call_update" && e.EventType != "tool_call") {
				continue
			}
			kind, _ := field(e.Params, "update.kind")
			status, _ := field(e.Params, "update.status")
			if (a.Kind == "" || kind == a.Kind) && (a.Status == "" || status == a.Status) {
				return nil
			}
		}
	case "any-of":
		for _, child := range a.Assertions {
			if validateAssertion(result, child) == nil {
				return nil
			}
		}
	default:
		return &UnsupportedError{Reason: fmt.Sprintf("assertion %q", a.Type)}
	}
	return fmt.Errorf("assertion %s has no matching evidence (method=%s event=%s path=%s)", a.Type, a.Method, a.EventType, a.Path)
}
func validatePermissionResponses(entries []TranscriptEntry, method string) error {
	if method != "session/request_permission" {
		return nil
	}
	for _, response := range responses(entries, method) {
		outcome, _ := field(response.Response, "outcome.outcome")
		if outcome == "cancelled" {
			continue
		}
		option, _ := field(response.Response, "outcome.optionId")
		if outcome != "selected" || option == nil {
			return fmt.Errorf("invalid permission outcome")
		}
		valid := false
		for _, req := range entries {
			if req.ConnectionID != response.ConnectionID || req.Direction == response.Direction || string(req.RequestID) != string(response.RequestID) {
				continue
			}
			options, _ := field(req.Params, "options")
			if list, ok := options.([]any); ok {
				for _, item := range list {
					m, ok := item.(map[string]any)
					if ok && m["optionId"] == option {
						valid = true
					}
				}
			}
		}
		if !valid {
			return fmt.Errorf("permission response selected option not offered")
		}
	}
	return nil
}

func entrySucceeded(entries []TranscriptEntry, request TranscriptEntry) bool {
	if !witnessed(request) {
		return false
	}
	if len(request.RequestID) == 0 {
		return true
	}
	for _, response := range responses(entries, request.Method) {
		if response.ConnectionID == request.ConnectionID && response.Direction != request.Direction && string(response.RequestID) == string(request.RequestID) {
			return true
		}
	}
	return false
}
