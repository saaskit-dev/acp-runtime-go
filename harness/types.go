package harness

type Case struct {
	Version              int         `json:"version"`
	ID                   string      `json:"id"`
	Kind                 string      `json:"kind"`
	Title                string      `json:"title"`
	ProtocolDependencies []string    `json:"protocolDependencies,omitempty"`
	Steps                []CaseStep  `json:"steps"`
	Assertions           []Assertion `json:"assertions,omitempty"`
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
}

type Assertion struct {
	Type       string      `json:"type"`
	Method     string      `json:"method,omitempty"`
	EventType  string      `json:"eventType,omitempty"`
	Assertions []Assertion `json:"assertions,omitempty"`
}

type Result struct {
	CaseID     string
	Transcript []TranscriptEntry
}

type TranscriptEntry struct {
	Method    string
	EventType string
}
