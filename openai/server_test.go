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
	body := `{"model":"simulator","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"hello"}]}`
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
	_, server := newTestAppServer(t)
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
	if got := parsed.Choices[0].Message.Role; got != "assistant" {
		t.Fatalf("message role = %q, want assistant", got)
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
	first := postJSON(t, server, "/v1/chat/completions", `{"model":"simulator","messages":[{"role":"system","content":"be terse"},{"role":"user","content":"hello"}]}`, map[string]string{
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

	second := postJSON(t, server, "/v1/chat/completions", `{"model":"simulator","messages":[{"role":"user","content":"hello again"}]}`, map[string]string{
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

func TestSessionRecordBusyState(t *testing.T) {
	record := &sessionRecord{managed: true}
	if !record.tryBegin() {
		t.Fatalf("first tryBegin() = false, want true")
	}
	if record.tryBegin() {
		t.Fatalf("second tryBegin() = true, want false while busy")
	}
	record.end(false, time.Minute)
	if !record.tryBegin() {
		t.Fatalf("tryBegin() after end = false, want true")
	}
	record.end(true, time.Minute)
	if record.tryBegin() {
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

func newTestAppServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	simulatorBin := buildSimulatorBinary(t)
	runtime := acp.NewRuntime(acp.NewStdioConnectionFactory(acp.StdioFactoryOptions{}), acp.RuntimeOptions{})
	app := NewServer(Config{
		Runtime:        runtime,
		DefaultAgentID: "simulator",
		CWD:            t.TempDir(),
		SessionTTL:     time.Minute,
		ResolveAgent: func(context.Context, string) (acp.Agent, error) {
			return acp.Agent{
				Type:    acp.LocalSimulatorAgentACPRegistryID,
				Command: simulatorBin,
				Args:    []string{"--auth-mode", "none", "--storage-dir", t.TempDir()},
			}, nil
		},
	})
	t.Cleanup(func() { _ = app.Close(context.Background()) })
	server := httptest.NewServer(app.Handler())
	t.Cleanup(server.Close)
	return app, server
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
