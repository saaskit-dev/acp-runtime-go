package harness

import "encoding/json"

type Case struct {
	Experimental         bool     `json:"experimental,omitempty"`
	Version              int      `json:"version"`
	ID                   string   `json:"id"`
	Kind                 string   `json:"kind"`
	Title                string   `json:"title"`
	ProtocolDependencies []string `json:"protocolDependencies,omitempty"`
	Agents               struct {
		Include []string `json:"include,omitempty"`
		Exclude []string `json:"exclude,omitempty"`
	} `json:"agents,omitempty"`
	Probes     map[string]Probe  `json:"probes,omitempty"`
	Fixtures   map[string]string `json:"fixtures,omitempty"`
	Steps      []CaseStep        `json:"steps"`
	Assertions []Assertion       `json:"assertions,omitempty"`
}
type Probe struct {
	Prompt string `json:"prompt,omitempty"`
	ModeID string `json:"modeId,omitempty"`
}
type CaseStep struct {
	Type          string `json:"type"`
	Prompt        string `json:"prompt,omitempty"`
	DefaultPrompt string `json:"defaultPrompt,omitempty"`
	TurnRef       string `json:"turnRef,omitempty"`
	EventType     string `json:"eventType,omitempty"`
	ModeID        string `json:"modeId,omitempty"`
	Key           string `json:"key,omitempty"`
	Value         any    `json:"value,omitempty"`
	Decision      string `json:"decision,omitempty"`
	TimeoutMS     int    `json:"timeoutMs,omitempty"`
	SkipIf        string `json:"skipIf,omitempty"`
}
type Assertion struct {
	Type       string      `json:"type"`
	Method     string      `json:"method,omitempty"`
	EventType  string      `json:"eventType,omitempty"`
	Path       string      `json:"path,omitempty"`
	EqualsSet  bool        `json:"-"`
	Equals     any         `json:"equals,omitempty"`
	NotEmpty   bool        `json:"notEmpty,omitempty"`
	First      string      `json:"first,omitempty"`
	Then       string      `json:"then,omitempty"`
	Min        *int        `json:"min,omitempty"`
	Max        *int        `json:"max,omitempty"`
	Kind       string      `json:"kind,omitempty"`
	Status     string      `json:"status,omitempty"`
	Assertions []Assertion `json:"assertions,omitempty"`
}
type Result struct {
	CaseID     string            `json:"caseId"`
	Status     string            `json:"status"`
	Reason     string            `json:"reason,omitempty"`
	Transcript []TranscriptEntry `json:"transcript"`
}

// TranscriptEntry is one observed JSON-RPC frame. Outbound attempts never count
// as evidence; write-success proves transport acceptance, not peer receipt.
// A matched successful response is required for request assertions.
type TranscriptEntry struct {
	Malformed    string          `json:"malformed,omitempty"`
	ConnectionID uint64          `json:"connectionId"`
	Sequence     uint64          `json:"sequence"`
	Direction    string          `json:"direction"`
	Phase        string          `json:"phase"`
	RequestID    json.RawMessage `json:"requestId,omitempty"`
	Method       string          `json:"method,omitempty"`
	EventType    string          `json:"eventType,omitempty"`
	SessionID    string          `json:"sessionId,omitempty"`
	Params       json.RawMessage `json:"params,omitempty"`
	Response     json.RawMessage `json:"response,omitempty"`
	Error        json.RawMessage `json:"error,omitempty"`
	Raw          json.RawMessage `json:"raw,omitempty"`
	Terminal     bool            `json:"terminal,omitempty"`
}
type UnsupportedError struct{ Reason string }

func (e *UnsupportedError) Error() string { return "unsupported: " + e.Reason }

func (a *Assertion) UnmarshalJSON(data []byte) error {
	type plain Assertion
	var out plain
	if err := json.Unmarshal(data, &out); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	_, out.EqualsSet = fields["equals"]
	*a = Assertion(out)
	return nil
}
