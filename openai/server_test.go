package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	acp "github.com/saaskit-dev/acp-runtime-go"
)

func TestChatCompletionsNonStreaming(t *testing.T) {
	_, server := newTestAppServer(t)
	body := `{"model":"simulator","messages":[{"role":"user","content":"hello"}]}`
	resp := postJSON(t, server, "/v1/chat/completions", body, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var parsed chatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if parsed.Object != "chat.completion" {
		t.Fatalf("Object = %q", parsed.Object)
	}
	if got := parsed.Choices[0].Message.Content; got != "OK" {
		t.Fatalf("content = %q, want OK", got)
	}
	if parsed.Usage == nil || parsed.Usage.TotalTokens == 0 {
		t.Fatalf("usage = %#v, want populated usage", parsed.Usage)
	}
}

func TestChatCompletionsStreaming(t *testing.T) {
	_, server := newTestAppServer(t)
	body := `{"model":"simulator/gpt","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"hello"}]}`
	resp := postJSON(t, server, "/v1/chat/completions", body, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); !strings.Contains(got, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want event-stream", got)
	}
	scanner := bufio.NewScanner(resp.Body)
	var sawText, sawDone bool
	var streamID string
	var sawUsage bool
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, `"content":"OK"`) {
			sawText = true
		}
		if strings.HasPrefix(line, "data: {") {
			var chunk chatCompletionResponse
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &chunk); err != nil {
				t.Fatalf("stream chunk JSON error = %v, line=%s", err, line)
			}
			if chunk.ID == "" {
				t.Fatalf("stream chunk missing id: %s", line)
			}
			if chunk.Model != "simulator/gpt" {
				t.Fatalf("stream chunk model = %q, want simulator/gpt", chunk.Model)
			}
			if streamID == "" {
				streamID = chunk.ID
			} else if chunk.ID != streamID {
				t.Fatalf("stream chunk id = %q, want stable %q", chunk.ID, streamID)
			}
			if chunk.Usage != nil {
				sawUsage = true
			}
		}
		if line == "data: [DONE]" {
			sawDone = true
			break
		}
	}
	if !sawText || !sawDone {
		t.Fatalf("stream sawText=%v sawDone=%v", sawText, sawDone)
	}
	if !sawUsage {
		t.Fatalf("stream_options.include_usage did not produce usage")
	}
}

func TestChatCompletionsAcceptsOpenAINodeGeneratedCreateParams(t *testing.T) {
	app, server := newTestAppServer(t)
	body := `{
		"messages":[{"content":"string","role":"developer","name":"name"}],
		"model":"gpt-5.4",
		"max_completion_tokens":0,
		"max_tokens":0,
		"metadata":{"foo":"string"},
		"modalities":["text"],
		"n":1,
		"response_format":{"type":"text"},
		"stop":"\n",
		"stream":false,
		"stream_options":{"include_obfuscation":true,"include_usage":true},
		"temperature":1,
		"tool_choice":"none",
		"tools":[{"function":{"name":"name","description":"description","parameters":{"foo":"bar"},"strict":true},"type":"function"}],
		"top_p":1,
		"user":"user-1234"
	}`
	resp := postJSON(t, server, "/v1/chat/completions", body, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s", resp.StatusCode, data)
	}
	var parsed chatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if parsed.Model != "gpt-5.4" {
		t.Fatalf("model = %q, want gpt-5.4", parsed.Model)
	}
	if got := app.resolvedAgents(); len(got) != 1 || got[0] != "codex" {
		t.Fatalf("resolved agents = %v, want [codex]", got)
	}
	if got := parsed.Choices[0].Message.Role; got != "assistant" {
		t.Fatalf("message role = %q, want assistant", got)
	}
}

func TestChatCompletionsSeparatesAgentAndModel(t *testing.T) {
	app, server := newTestAppServer(t)
	resp := postJSON(t, server, "/v1/chat/completions", `{"model":"claude/sonnet","messages":[{"role":"user","content":"hello"}]}`, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s", resp.StatusCode, data)
	}
	var parsed chatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if parsed.Model != "claude/sonnet" {
		t.Fatalf("response model = %q, want claude/sonnet", parsed.Model)
	}
	if got := app.resolvedAgents(); len(got) != 1 || got[0] != "claude" {
		t.Fatalf("resolved agents = %v, want [claude]", got)
	}
}

func TestChatCompletionsSupportsACPModelRoute(t *testing.T) {
	app, server := newTestAppServer(t)
	resp := postJSON(t, server, "/v1/chat/completions", `{"model":"codex/gpt-5.4","messages":[{"role":"user","content":"hello"}]}`, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s", resp.StatusCode, data)
	}
	if got := app.resolvedAgents(); len(got) != 1 || got[0] != "codex" {
		t.Fatalf("resolved agents = %v, want [codex]", got)
	}
}

func TestResponsesNonStreaming(t *testing.T) {
	app, server := newTestAppServer(t)
	body := `{"model":"claude/sonnet","instructions":"be terse","input":"hello"}`
	resp := postJSON(t, server, "/v1/responses", body, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s", resp.StatusCode, data)
	}
	var parsed responseObject
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if parsed.Object != "response" || parsed.Status != "completed" {
		t.Fatalf("response object/status = %q/%q", parsed.Object, parsed.Status)
	}
	if parsed.ID == "" {
		t.Fatalf("missing response id")
	}
	if parsed.Model != "claude/sonnet" {
		t.Fatalf("model = %q, want claude/sonnet", parsed.Model)
	}
	if parsed.OutputText != "OK" {
		t.Fatalf("output_text = %q, want OK", parsed.OutputText)
	}
	if len(parsed.Output) != 1 || len(parsed.Output[0].Content) != 1 || parsed.Output[0].Content[0].Text != "OK" {
		t.Fatalf("output = %#v, want message output_text OK", parsed.Output)
	}
	if parsed.Usage == nil || parsed.Usage.TotalTokens == 0 {
		t.Fatalf("usage = %#v, want populated usage", parsed.Usage)
	}
	if got := app.resolvedAgents(); len(got) != 1 || got[0] != "claude" {
		t.Fatalf("resolved agents = %v, want [claude]", got)
	}
}

func TestResponsesStreaming(t *testing.T) {
	_, server := newTestAppServer(t)
	body := `{"model":"codex/gpt-5.5","stream":true,"input":[{"role":"user","content":[{"type":"input_text","text":"hello"}]}]}`
	resp := postJSON(t, server, "/v1/responses", body, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s", resp.StatusCode, data)
	}
	if got := resp.Header.Get("Content-Type"); !strings.Contains(got, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want event-stream", got)
	}
	scanner := bufio.NewScanner(resp.Body)
	var sawCreated, sawDelta, sawCompleted, sawDone bool
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "event: response.created":
			sawCreated = true
		case line == "event: response.output_text.delta":
			sawDelta = true
		case line == "event: response.completed":
			sawCompleted = true
		case line == "data: [DONE]":
			sawDone = true
		}
	}
	if !sawCreated || !sawDelta || !sawCompleted || !sawDone {
		t.Fatalf("stream saw created=%v delta=%v completed=%v done=%v", sawCreated, sawDelta, sawCompleted, sawDone)
	}
}

func TestResponsesPreviousResponseIDReusesManagedSession(t *testing.T) {
	_, server := newTestAppServer(t)
	first := postJSON(t, server, "/v1/responses", `{"model":"simulator/gpt","input":"hello"}`, nil)
	defer first.Body.Close()
	if first.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(first.Body)
		t.Fatalf("first status = %d body=%s", first.StatusCode, data)
	}
	var firstParsed responseObject
	if err := json.NewDecoder(first.Body).Decode(&firstParsed); err != nil {
		t.Fatalf("Decode first error = %v", err)
	}
	if firstParsed.ID == "" {
		t.Fatalf("missing first response id")
	}
	sessionID := first.Header.Get(headerSessionID)
	if sessionID == "" {
		t.Fatalf("missing ACP session header")
	}

	secondBody := `{"model":"simulator/gpt","previous_response_id":"` + firstParsed.ID + `","input":"next"}`
	second := postJSON(t, server, "/v1/responses", secondBody, nil)
	defer second.Body.Close()
	if second.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(second.Body)
		t.Fatalf("second status = %d body=%s", second.StatusCode, data)
	}
	if got := second.Header.Get(headerSessionID); got != sessionID {
		t.Fatalf("session header = %q, want reused %q", got, sessionID)
	}

	listReq, err := http.NewRequest(http.MethodGet, server.URL+"/v1/acp/sessions", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	listResp, err := server.Client().Do(listReq)
	if err != nil {
		t.Fatalf("Do() list error = %v", err)
	}
	defer listResp.Body.Close()
	var list sessionListResponse
	if err := json.NewDecoder(listResp.Body).Decode(&list); err != nil {
		t.Fatalf("Decode list error = %v", err)
	}
	if len(list.Data) != 1 {
		t.Fatalf("session list length = %d, want 1", len(list.Data))
	}
}

func TestResponsesStoreFalseUsesTemporarySession(t *testing.T) {
	_, server := newTestAppServer(t)
	resp := postJSON(t, server, "/v1/responses", `{"model":"simulator","store":false,"input":"hello"}`, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s", resp.StatusCode, data)
	}
	listReq, err := http.NewRequest(http.MethodGet, server.URL+"/v1/acp/sessions", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	listResp, err := server.Client().Do(listReq)
	if err != nil {
		t.Fatalf("Do() list error = %v", err)
	}
	defer listResp.Body.Close()
	var list sessionListResponse
	if err := json.NewDecoder(listResp.Body).Decode(&list); err != nil {
		t.Fatalf("Decode list error = %v", err)
	}
	if len(list.Data) != 0 {
		t.Fatalf("session list length = %d, want 0", len(list.Data))
	}
}

func TestResponsesRejectsUnknownPreviousResponseID(t *testing.T) {
	_, server := newTestAppServer(t)
	resp := postJSON(t, server, "/v1/responses", `{"model":"simulator","previous_response_id":"resp_missing","input":"hello"}`, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s", resp.StatusCode, data)
	}
	var parsed openAIErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if parsed.Error.Param != "previous_response_id" {
		t.Fatalf("param = %q, want previous_response_id", parsed.Error.Param)
	}
}

func TestResponsesRoutesBareGPTModelToCodex(t *testing.T) {
	app, server := newTestAppServer(t)
	resp := postJSON(t, server, "/v1/responses", `{"model":"gpt-5.5","store":false,"input":"hello"}`, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s", resp.StatusCode, data)
	}
	if got := app.resolvedAgents(); len(got) != 1 || got[0] != "codex" {
		t.Fatalf("resolved agents = %v, want [codex]", got)
	}
}

func TestResponsesRoutesBareClaudeModelToClaude(t *testing.T) {
	app, server := newTestAppServer(t)
	resp := postJSON(t, server, "/v1/responses", `{"model":"claude-sonnet-4-6","store":false,"input":"hello"}`, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s", resp.StatusCode, data)
	}
	if got := app.resolvedAgents(); len(got) != 1 || got[0] != "claude" {
		t.Fatalf("resolved agents = %v, want [claude]", got)
	}
}

func TestResponsesAcceptsReasoningEffort(t *testing.T) {
	_, server := newTestAppServer(t)
	resp := postJSON(t, server, "/v1/responses", `{"model":"gpt-5.5","store":false,"reasoning":{"effort":"xhigh","summary":"auto"},"input":"hello"}`, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s", resp.StatusCode, data)
	}
}

func TestResponsesRejectsUnsupportedReasoningEffort(t *testing.T) {
	_, server := newTestAppServer(t)
	resp := postJSON(t, server, "/v1/responses", `{"model":"gpt-5.5","store":false,"reasoning":{"effort":"ultracode"},"input":"hello"}`, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s", resp.StatusCode, data)
	}
	var parsed openAIErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if parsed.Error.Param != "reasoning.effort" {
		t.Fatalf("param = %q, want reasoning.effort", parsed.Error.Param)
	}
}

func TestResponsesAcceptsExplicitAgentModelRoute(t *testing.T) {
	app, server := newTestAppServer(t)
	resp := postJSON(t, server, "/v1/responses", `{"model":"codex/gpt-5.5","store":false,"input":"hello"}`, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s", resp.StatusCode, data)
	}
	if got := app.resolvedAgents(); len(got) != 1 || got[0] != "codex" {
		t.Fatalf("resolved agents = %v, want [codex]", got)
	}
}

func TestModelsReturnsConfiguredRoutableModelIDs(t *testing.T) {
	_, server := newTestAppServer(t)
	req, err := http.NewRequest(http.MethodGet, server.URL+"/v1/models", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var parsed modelListResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	var ids []string
	for _, item := range parsed.Data {
		ids = append(ids, item.ID)
	}
	for _, want := range []string{"claude-sonnet-4-6", "gpt-5.5"} {
		if !containsString(ids, want) {
			t.Fatalf("model ids = %v, missing %q", ids, want)
		}
	}
	for _, hidden := range []string{"claude/sonnet", "codex/gpt-5.5"} {
		if containsString(ids, hidden) {
			t.Fatalf("model ids = %v, should not expose internal id %q", ids, hidden)
		}
	}
}

func TestPublicModelIDsNormalizeACPModelNames(t *testing.T) {
	got := publicModelIDs([]string{
		"claude/default",
		"claude/sonnet[1m]",
		"claude/opus[1m]",
		"claude/haiku",
		"codex/gpt-5.5",
		"codex/gpt-5.4-mini",
	})
	want := []string{"claude-sonnet-4-6", "claude-opus-4-8", "claude-haiku-4-5-20251001", "gpt-5.5", "gpt-5.4-mini"}
	if len(got) != len(want) {
		t.Fatalf("public models = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("public models = %v, want %v", got, want)
		}
	}
}

func TestModelsDiscoveryPrewarmsCacheAfterStart(t *testing.T) {
	app, server := newDiscoveryTestAppServer(t)
	waitForModelCache(t, app.Server)
	before := app.resolvedAgents()

	req, err := http.NewRequest(http.MethodGet, server.URL+"/v1/models", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s", resp.StatusCode, data)
	}
	var parsed modelListResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	after := app.resolvedAgents()
	if len(after) != len(before) {
		t.Fatalf("model discovery ran again after prewarm: before=%v after=%v", before, after)
	}
	var ids []string
	for _, item := range parsed.Data {
		ids = append(ids, item.ID)
	}
	for _, want := range []string{"gpt", "claude", "gemini"} {
		if !containsString(ids, want) {
			t.Fatalf("model ids = %v, missing %q", ids, want)
		}
	}
}

func TestModelsDiscoversACPModelConfigOptions(t *testing.T) {
	app, server := newDiscoveryTestAppServer(t)
	req, err := http.NewRequest(http.MethodGet, server.URL+"/v1/models", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s", resp.StatusCode, data)
	}
	var parsed modelListResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	var ids []string
	for _, item := range parsed.Data {
		ids = append(ids, item.ID)
	}
	for _, want := range []string{"gpt", "claude", "gemini"} {
		if !containsString(ids, want) {
			t.Fatalf("model ids = %v, missing %q", ids, want)
		}
	}
	app.Server.mu.Lock()
	sessionCount := len(app.sessions)
	app.Server.mu.Unlock()
	if sessionCount != 0 {
		t.Fatalf("discovery registered %d persistent sessions, want 0", sessionCount)
	}
}

func TestChatCompletionsRejectsUnsupportedOpenAIParams(t *testing.T) {
	_, server := newTestAppServer(t)
	tests := []struct {
		name  string
		body  string
		param string
	}{
		{
			name:  "multiple choices",
			body:  `{"model":"simulator","n":2,"messages":[{"role":"user","content":"hello"}]}`,
			param: "n",
		},
		{
			name:  "audio modality",
			body:  `{"model":"simulator","modalities":["text","audio"],"messages":[{"role":"user","content":"hello"}]}`,
			param: "modalities",
		},
		{
			name:  "json schema response",
			body:  `{"model":"simulator","response_format":{"type":"json_schema"},"messages":[{"role":"user","content":"hello"}]}`,
			param: "response_format",
		},
		{
			name:  "tool calling",
			body:  `{"model":"simulator","tool_choice":"auto","tools":[{"type":"function","function":{"name":"get_weather","parameters":{"type":"object"}}}],"messages":[{"role":"user","content":"weather"}]}`,
			param: "tools",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := postJSON(t, server, "/v1/chat/completions", tt.body, nil)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				data, _ := io.ReadAll(resp.Body)
				t.Fatalf("status = %d body=%s", resp.StatusCode, data)
			}
			var parsed openAIErrorResponse
			if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
				t.Fatalf("Decode() error = %v", err)
			}
			if parsed.Error.Param != tt.param {
				t.Fatalf("param = %q, want %q", parsed.Error.Param, tt.param)
			}
		})
	}
}

func TestPersistentSessionReturnsAndReusesSessionID(t *testing.T) {
	_, server := newTestAppServer(t)
	first := postJSON(t, server, "/v1/chat/completions", `{"model":"test-model","messages":[{"role":"system","content":"be terse"},{"role":"user","content":"hello"}]}`, map[string]string{
		headerSessionMode: "persistent",
	})
	defer first.Body.Close()
	_, _ = io.ReadAll(first.Body)
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first status = %d", first.StatusCode)
	}
	sessionID := first.Header.Get(headerSessionID)
	if sessionID == "" {
		t.Fatalf("missing %s", headerSessionID)
	}

	second := postJSON(t, server, "/v1/chat/completions", `{"model":"test-model","messages":[{"role":"user","content":"hello again"}]}`, map[string]string{
		headerSessionID: sessionID,
	})
	defer second.Body.Close()
	if second.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(second.Body)
		t.Fatalf("second status = %d body=%s", second.StatusCode, data)
	}
	if got := second.Header.Get(headerSessionID); got != sessionID {
		t.Fatalf("session header = %q, want %q", got, sessionID)
	}

	listReq, err := http.NewRequest(http.MethodGet, server.URL+"/v1/acp/sessions", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	listResp, err := server.Client().Do(listReq)
	if err != nil {
		t.Fatalf("Do() list error = %v", err)
	}
	defer listResp.Body.Close()
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d", listResp.StatusCode)
	}
	var list sessionListResponse
	if err := json.NewDecoder(listResp.Body).Decode(&list); err != nil {
		t.Fatalf("Decode list error = %v", err)
	}
	if len(list.Data) != 1 {
		t.Fatalf("session list length = %d, want 1", len(list.Data))
	}
	if list.Data[0].ID != sessionID {
		t.Fatalf("session list id = %q, want %q", list.Data[0].ID, sessionID)
	}
	if list.Data[0].ACPSessionID == "" {
		t.Fatalf("missing acp_session_id")
	}
	if list.Data[0].Model != "test-model" {
		t.Fatalf("session list model = %q, want test-model", list.Data[0].Model)
	}
}

func TestPersistentSessionRejectsDifferentModel(t *testing.T) {
	_, server := newTestAppServer(t)
	first := postJSON(t, server, "/v1/chat/completions", `{"model":"model-a","messages":[{"role":"user","content":"hello"}]}`, map[string]string{
		headerSessionMode: "persistent",
	})
	defer first.Body.Close()
	_, _ = io.ReadAll(first.Body)
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first status = %d", first.StatusCode)
	}
	sessionID := first.Header.Get(headerSessionID)
	if sessionID == "" {
		t.Fatalf("missing session id")
	}
	second := postJSON(t, server, "/v1/chat/completions", `{"model":"model-b","messages":[{"role":"user","content":"hello again"}]}`, map[string]string{
		headerSessionID: sessionID,
	})
	defer second.Body.Close()
	if second.StatusCode != http.StatusConflict {
		data, _ := io.ReadAll(second.Body)
		t.Fatalf("second status = %d body=%s", second.StatusCode, data)
	}
}

func TestDeleteSessionIsOwnerScoped(t *testing.T) {
	app, server := newTestAppServer(t)
	sessionID := createPersistentSession(t, server, "owner-a")

	resp := deleteSession(t, server, sessionID, "owner-b")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("delete by other owner status = %d, want 404", resp.StatusCode)
	}
	if _, ok := app.getSession(sessionID); !ok {
		t.Fatalf("session was removed by a different owner")
	}

	resp = deleteSession(t, server, sessionID, "owner-a")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("delete by owner status = %d body=%s", resp.StatusCode, data)
	}
	if _, ok := app.getSession(sessionID); ok {
		t.Fatalf("session still registered after owner delete")
	}
}

func TestCleanupExpiredDeletesOnlyRegisteredManagedSessions(t *testing.T) {
	app, server := newTestAppServer(t)
	sessionID := createPersistentSession(t, server, "owner-a")
	record, ok := app.getSession(sessionID)
	if !ok {
		t.Fatalf("session not registered")
	}
	record.expiresAt = time.Now().Add(-time.Second)

	app.cleanupExpired()
	if _, ok := app.getSession(sessionID); ok {
		t.Fatalf("expired session still registered")
	}
}

func TestCleanupExpiredSkipsBusySessions(t *testing.T) {
	app, server := newTestAppServer(t)
	sessionID := createPersistentSession(t, server, "owner-a")
	record, ok := app.getSession(sessionID)
	if !ok {
		t.Fatalf("session not registered")
	}
	if !record.tryBegin(time.Minute) {
		t.Fatal("tryBegin() = false, want true")
	}
	// Expire wall-clock TTL while the turn is still busy.
	record.mu.Lock()
	record.expiresAt = time.Now().Add(-time.Second)
	record.mu.Unlock()

	app.cleanupExpired()
	if _, ok := app.getSession(sessionID); !ok {
		t.Fatal("busy session was cleaned up despite active turn")
	}
	record.end(false, time.Minute)
	// Force expiry again after the turn ends; cleanup should now remove it.
	record.mu.Lock()
	record.expiresAt = time.Now().Add(-time.Second)
	record.mu.Unlock()
	app.cleanupExpired()
	if _, ok := app.getSession(sessionID); ok {
		t.Fatal("expired idle session still registered after cleanup")
	}
}

func TestTryBeginRefreshesExpiry(t *testing.T) {
	record := &sessionRecord{
		managed:   true,
		expiresAt: time.Now().Add(time.Second),
	}
	before := record.expiresAt
	if !record.tryBegin(30 * time.Minute) {
		t.Fatal("tryBegin() = false, want true")
	}
	if !record.expiresAt.After(before.Add(time.Minute)) {
		t.Fatalf("expiresAt not refreshed on tryBegin: before=%v after=%v", before, record.expiresAt)
	}
	record.end(false, time.Minute)
}

func TestSessionRecordBusyState(t *testing.T) {
	record := &sessionRecord{managed: true}
	if !record.tryBegin(time.Minute) {
		t.Fatalf("first tryBegin() = false, want true")
	}
	if record.tryBegin(time.Minute) {
		t.Fatalf("second tryBegin() = true, want false while busy")
	}
	record.end(false, time.Minute)
	if !record.tryBegin(time.Minute) {
		t.Fatalf("tryBegin() after end = false, want true")
	}
	record.end(true, time.Minute)
	if record.tryBegin(time.Minute) {
		t.Fatalf("tryBegin() after taint = true, want false")
	}
}

func TestBuildPromptIncrementalAndReplay(t *testing.T) {
	req := chatCompletionRequest{
		Messages: []chatMessage{
			{Role: "system", Content: messageContent{Text: "rules"}},
			{Role: "user", Content: messageContent{Text: "first"}},
			{Role: "assistant", Content: messageContent{Text: "previous"}},
			{Role: "user", Content: messageContent{Text: "second"}},
		},
	}
	if got := buildPrompt(req, true); got != "second" {
		t.Fatalf("incremental prompt = %q, want second", got)
	}
	got := buildPrompt(req, false)
	for _, want := range []string{"[System]\nrules", "first", "[Previous assistant response]\nprevious", "second"} {
		if !strings.Contains(got, want) {
			t.Fatalf("replay prompt = %q, missing %q", got, want)
		}
	}
}

func TestBuildPromptMapsDeveloperMessages(t *testing.T) {
	req := chatCompletionRequest{
		Messages: []chatMessage{{Role: "developer", Content: messageContent{Text: "follow policy"}}},
	}
	if got := buildPrompt(req, false); got != "[Developer]\nfollow policy" {
		t.Fatalf("prompt = %q", got)
	}
}

func newTestServer(t *testing.T) *httptest.Server {
	_, server := newTestAppServer(t)
	return server
}

type testOpenAIApp struct {
	*Server

	mu     sync.Mutex
	agents []string
}

func (a *testOpenAIApp) resolvedAgents() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.agents...)
}

func newTestAppServer(t *testing.T) (*testOpenAIApp, *httptest.Server) {
	t.Helper()
	simulatorBin := buildSimulatorBinary(t)
	runtime := acp.NewRuntime(acp.NewStdioConnectionFactory(acp.StdioFactoryOptions{}), acp.RuntimeOptions{})
	app := &testOpenAIApp{}
	serverApp := NewServer(Config{
		Runtime:        runtime,
		DefaultAgentID: "simulator",
		CWD:            t.TempDir(),
		SessionTTL:     time.Minute,
		Models:         []string{"simulator", "simulator/gpt", "claude/sonnet", "codex/gpt-5.4", "codex/gpt-5.5", "gpt-5.4", "test-model", "model-a", "model-b"},
		ResolveAgent: func(_ context.Context, agentID string) (acp.Agent, error) {
			app.mu.Lock()
			app.agents = append(app.agents, agentID)
			app.mu.Unlock()
			return acp.Agent{
				Type:    acp.LocalSimulatorAgentACPRegistryID,
				Command: simulatorBin,
				Args:    []string{"--auth-mode", "none", "--storage-dir", t.TempDir()},
			}, nil
		},
	})
	app.Server = serverApp
	t.Cleanup(func() { _ = app.Close(context.Background()) })
	server := httptest.NewServer(app.Handler())
	t.Cleanup(server.Close)
	return app, server
}

func newDiscoveryTestAppServer(t *testing.T) (*testOpenAIApp, *httptest.Server) {
	t.Helper()
	simulatorBin := buildSimulatorBinary(t)
	runtime := acp.NewRuntime(acp.NewStdioConnectionFactory(acp.StdioFactoryOptions{}), acp.RuntimeOptions{})
	app := &testOpenAIApp{}
	serverApp := NewServer(Config{
		Runtime:           runtime,
		DefaultAgentID:    "simulator",
		CWD:               t.TempDir(),
		SessionTTL:        time.Minute,
		Agents:            []string{"simulator"},
		DiscoverModels:    true,
		ModelDiscoveryTTL: time.Minute,
		ResolveAgent: func(_ context.Context, agentID string) (acp.Agent, error) {
			app.mu.Lock()
			app.agents = append(app.agents, agentID)
			app.mu.Unlock()
			return acp.Agent{
				Type:    acp.LocalSimulatorAgentACPRegistryID,
				Command: simulatorBin,
				Args:    []string{"--auth-mode", "none", "--storage-dir", t.TempDir()},
			}, nil
		},
	})
	app.Server = serverApp
	t.Cleanup(func() { _ = app.Close(context.Background()) })
	server := httptest.NewServer(app.Handler())
	t.Cleanup(server.Close)
	return app, server
}

func waitForModelCache(t *testing.T, server *Server) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		server.modelMu.Lock()
		ready := time.Now().Before(server.modelCacheExpiry) && len(server.modelCache) > 0
		server.modelMu.Unlock()
		if ready {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	server.modelMu.Lock()
	cache := append([]string(nil), server.modelCache...)
	expiry := server.modelCacheExpiry
	server.modelMu.Unlock()
	t.Fatalf("model discovery cache was not prewarmed: cache=%v expiry=%v", cache, expiry)
}

func postJSON(t *testing.T, server *httptest.Server, path string, body string, headers map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, server.URL+path, bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	return resp
}

func createPersistentSession(t *testing.T, server *httptest.Server, owner string) string {
	t.Helper()
	headers := map[string]string{
		headerSessionMode: "persistent",
		"Authorization":   "Bearer " + owner,
	}
	resp := postJSON(t, server, "/v1/chat/completions", `{"model":"simulator","messages":[{"role":"user","content":"hello"}]}`, headers)
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create session status = %d", resp.StatusCode)
	}
	sessionID := resp.Header.Get(headerSessionID)
	if sessionID == "" {
		t.Fatalf("missing session id")
	}
	return sessionID
}

func deleteSession(t *testing.T, server *httptest.Server, sessionID string, owner string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodDelete, server.URL+"/v1/acp/sessions/"+sessionID, nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+owner)
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("Do() delete error = %v", err)
	}
	return resp
}

func buildSimulatorBinary(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "acp-simulator-agent")
	cmd := exec.Command("go", "build", "-o", path, "../cmd/acp-simulator-agent")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build simulator: %v\n%s", err, string(output))
	}
	return path
}
