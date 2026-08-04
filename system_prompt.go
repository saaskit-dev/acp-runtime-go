package acpruntime

// Host-facing system-prompt injection.
//
// Callers declare replace/append intent only through StartSessionOptions.Meta
// (preferably via WithSystemPrompt / WithAppendSystemPrompt). The runtime
// profile layer projects that logical intent onto each provider; hosts must not
// set provider-specific env vars, CLI flags, or Agent.Args for system prompts.

// WithSystemPrompt builds Meta that replaces the provider system prompt.
// Pass the result as StartSessionOptions.Meta, or merge with other Meta keys
// via MergeMeta.
func WithSystemPrompt(text string) map[string]any {
	return map[string]any{SystemPromptMetaKey: text}
}

// WithAppendSystemPrompt builds Meta that appends to the provider system prompt.
// Pass the result as StartSessionOptions.Meta, or merge with other Meta keys
// via MergeMeta. Mutually exclusive with a non-empty WithSystemPrompt value.
func WithAppendSystemPrompt(text string) map[string]any {
	return map[string]any{AppendSystemPromptMetaKey: text}
}

// MergeMeta shallow-merges Meta maps left-to-right (later keys win). Nil maps
// are ignored. Use this to combine WithSystemPrompt / WithAppendSystemPrompt
// with other session metadata.
func MergeMeta(parts ...map[string]any) map[string]any {
	var out map[string]any
	for _, part := range parts {
		if len(part) == 0 {
			continue
		}
		if out == nil {
			out = make(map[string]any, len(part))
		}
		for key, value := range part {
			out[key] = value
		}
	}
	return out
}
