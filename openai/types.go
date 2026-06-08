package openai

import (
	"encoding/json"
	"fmt"
	"strings"
)

type chatCompletionRequest struct {
	Model               string                     `json:"model"`
	Messages            []chatMessage              `json:"messages"`
	Stream              bool                       `json:"stream,omitempty"`
	MaxTokens           *int                       `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int                       `json:"max_completion_tokens,omitempty"`
	Temperature         *float64                   `json:"temperature,omitempty"`
	TopP                *float64                   `json:"top_p,omitempty"`
	Stop                any                        `json:"stop,omitempty"`
	Metadata            map[string]any             `json:"metadata,omitempty"`
	User                string                     `json:"user,omitempty"`
	Tools               []json.RawMessage          `json:"tools,omitempty"`
	ToolChoice          any                        `json:"tool_choice,omitempty"`
	ResponseFormat      *responseFormat            `json:"response_format,omitempty"`
	StreamOptions       *streamOptions             `json:"stream_options,omitempty"`
	Modalities          []string                   `json:"modalities,omitempty"`
	N                   *int                       `json:"n,omitempty"`
	Extra               map[string]json.RawMessage `json:"-"`
}

type chatMessage struct {
	Role    string         `json:"role"`
	Content messageContent `json:"content"`
	Name    string         `json:"name,omitempty"`
}

type messageContent struct {
	Text  string
	Parts []messagePart
}

type messagePart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL any    `json:"image_url,omitempty"`
}

type responseFormat struct {
	Type string `json:"type"`
}

type streamOptions struct {
	IncludeUsage       bool `json:"include_usage,omitempty"`
	IncludeObfuscation bool `json:"include_obfuscation,omitempty"`
}

func (c *messageContent) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		c.Text = text
		return nil
	}
	var parts []messagePart
	if err := json.Unmarshal(data, &parts); err == nil {
		c.Parts = parts
		return nil
	}
	if string(data) == "null" {
		return nil
	}
	return fmt.Errorf("unsupported message content")
}

func (c messageContent) text() string {
	if c.Text != "" {
		return c.Text
	}
	var parts []string
	for _, part := range c.Parts {
		switch part.Type {
		case "text", "input_text":
			if part.Text != "" {
				parts = append(parts, part.Text)
			}
		case "image_url", "input_image":
			parts = append(parts, "[image]")
		}
	}
	return strings.Join(parts, "\n")
}

type chatCompletionResponse struct {
	ID                string                 `json:"id"`
	Object            string                 `json:"object"`
	Created           int64                  `json:"created"`
	Model             string                 `json:"model"`
	Choices           []chatCompletionChoice `json:"choices"`
	Usage             *openAIUsage           `json:"usage,omitempty"`
	SystemFingerprint string                 `json:"system_fingerprint,omitempty"`
}

type chatCompletionChoice struct {
	Index        int            `json:"index"`
	Message      *choiceMessage `json:"message,omitempty"`
	Delta        *choiceDelta   `json:"delta,omitempty"`
	FinishReason *string        `json:"finish_reason"`
}

type choiceMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type choiceDelta struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}

type openAIUsage struct {
	PromptTokens     uint64 `json:"prompt_tokens"`
	CompletionTokens uint64 `json:"completion_tokens"`
	TotalTokens      uint64 `json:"total_tokens"`
}

type openAIErrorResponse struct {
	Error openAIError `json:"error"`
}

type openAIError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Param   string `json:"param,omitempty"`
	Code    string `json:"code,omitempty"`
}

type modelListResponse struct {
	Object string        `json:"object"`
	Data   []modelObject `json:"data"`
}

type modelObject struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

type sessionListResponse struct {
	Object string             `json:"object"`
	Data   []sessionListEntry `json:"data"`
}

type sessionListEntry struct {
	ID           string `json:"id"`
	ACPSessionID string `json:"acp_session_id"`
	Object       string `json:"object"`
	Agent        string `json:"agent"`
	CWD          string `json:"cwd"`
	Busy         bool   `json:"busy"`
	Tainted      bool   `json:"tainted"`
	CreatedAt    int64  `json:"created_at"`
	LastSeenAt   int64  `json:"last_seen_at"`
	ExpiresAt    int64  `json:"expires_at"`
	Fingerprint  string `json:"fingerprint"`
}
