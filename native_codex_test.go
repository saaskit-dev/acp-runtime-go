package acpruntime

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestFakeCodexAppServer is a helper PROCESS (not a real test): it speaks the
// codex app-server wire protocol we probed live from codex-cli 0.153.4, so
// the native transport can be exercised end-to-end without the real CLI.
// It is spawned by TestCodexNativeTransportEndToEnd via the standard
// GO_WANT_HELPER_PROCESS pattern.
func TestFakeCodexAppServer(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	out := bufio.NewWriter(os.Stdout)
	threadCounter := 0
	turnCounter := 0
	threadIDs := map[int]string{}
	write := func(v any) {
		data, err := json.Marshal(v)
		if err != nil {
			return
		}
		_, _ = out.Write(data)
		_ = out.WriteByte('\n')
		_ = out.Flush()
	}
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var msg struct {
			ID     *json.Number    `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}
		if msg.Method == "" || msg.ID == nil {
			continue // notifications (initialized, turn/interrupt): ignore
		}
		switch msg.Method {
		case "initialize":
			write(map[string]any{"jsonrpc": "2.0", "id": *msg.ID, "result": map[string]any{"userAgent": "fake-codex/0.153.4"}})
		case "thread/start":
			var req struct {
				Model string `json:"model"`
			}
			_ = json.Unmarshal(msg.Params, &req)
			model := firstNonEmpty(req.Model, "fake-model")
			threadCounter++
			threadIDs[threadCounter] = fmt.Sprintf("fake-thread-%d", threadCounter)
			write(map[string]any{"jsonrpc": "2.0", "id": *msg.ID, "result": map[string]any{
				"model": model, "thread": map[string]any{"id": threadIDs[threadCounter]}}})
		case "thread/resume":
			var req struct {
				ThreadID string `json:"threadId"`
				Model    string `json:"model"`
			}
			_ = json.Unmarshal(msg.Params, &req)
			if req.ThreadID == "gone-thread" {
				write(map[string]any{"jsonrpc": "2.0", "id": *msg.ID,
					"error": map[string]any{"code": -32000, "message": "thread not found"}})
				return
			}
			write(map[string]any{"jsonrpc": "2.0", "id": *msg.ID, "result": map[string]any{
				"model": firstNonEmpty(req.Model, "fake-model"), "thread": map[string]any{"id": req.ThreadID}}})
		case "turn/start":
			var tr struct {
				ThreadID string `json:"threadId"`
			}
			_ = json.Unmarshal(msg.Params, &tr)
			tid := tr.ThreadID
			turnCounter++
			turnID := fmt.Sprintf("fake-turn-%d", turnCounter)
			write(map[string]any{"jsonrpc": "2.0", "id": *msg.ID, "result": map[string]any{
				"turn": map[string]any{"id": turnID, "items": []any{}, "status": "inProgress"}}})
			evt := func(method string, extra map[string]any) map[string]any {
				p := map[string]any{"threadId": tid, "turnId": turnID}
				for k, v := range extra {
					p[k] = v
				}
				return map[string]any{"jsonrpc": "2.0", "method": method, "params": p}
			}
			write(evt("item/started", map[string]any{"item": map[string]any{"type": "commandExecution", "id": "cmd_1", "command": "echo hi"}}))
			write(evt("item/agentMessage/delta", map[string]any{"itemId": "msg_1", "delta": "FAKE_"}))
			write(evt("item/agentMessage/delta", map[string]any{"itemId": "msg_1", "delta": "HELLO"}))
			write(evt("item/agentMessage/delta", map[string]any{"itemId": "msg_1", "delta": " PID=" + fmt.Sprint(os.Getpid())}))
			write(evt("item/completed", map[string]any{"item": map[string]any{"type": "commandExecution", "id": "cmd_1"}}))
			write(evt("item/completed", map[string]any{"item": map[string]any{"type": "agentMessage", "id": "msg_1", "text": "FAKE_HELLO PID=" + fmt.Sprint(os.Getpid())}}))
			write(evt("rawResponse/completed", map[string]any{"threadId": tid, "usage": map[string]any{"totalTokens": 100, "inputTokens": 90, "cachedInputTokens": 10, "cacheWriteInputTokens": 0, "outputTokens": 10, "reasoningOutputTokens": 2}}))
			write(evt("turn/completed", map[string]any{"turn": map[string]any{"id": turnID, "items": []any{}, "status": "completed"}}))
			// Fire the event stream exactly like the real app-server: message
			// deltas, one tool execution, usage, then settle.
			write(map[string]any{"jsonrpc": "2.0", "method": "item/started", "params": map[string]any{
				"item": map[string]any{"type": "commandExecution", "id": "cmd_1", "command": "echo hi"}}})
			write(map[string]any{"jsonrpc": "2.0", "method": "item/agentMessage/delta", "params": map[string]any{
				"itemId": "msg_1", "delta": "FAKE_"}})
			write(map[string]any{"jsonrpc": "2.0", "method": "item/agentMessage/delta", "params": map[string]any{
				"itemId": "msg_1", "delta": "HELLO"}})
			write(map[string]any{"jsonrpc": "2.0", "method": "item/agentMessage/delta", "params": map[string]any{
				"itemId": "msg_1", "delta": fmt.Sprintf(" PID=%d", os.Getpid())}})
			write(map[string]any{"jsonrpc": "2.0", "method": "item/completed", "params": map[string]any{
				"item": map[string]any{"type": "commandExecution", "id": "cmd_1"}}})
			write(map[string]any{"jsonrpc": "2.0", "method": "item/completed", "params": map[string]any{
				"item": map[string]any{"type": "agentMessage", "id": "msg_1", "text": "FAKE_HELLO"}}})
			write(map[string]any{"jsonrpc": "2.0", "method": "rawResponse/completed", "params": map[string]any{
				"usage": map[string]any{"totalTokens": 100, "inputTokens": 90, "cachedInputTokens": 10,
					"cacheWriteInputTokens": 0, "outputTokens": 10, "reasoningOutputTokens": 2}}})
			write(map[string]any{"jsonrpc": "2.0", "method": "turn/completed", "params": map[string]any{
				"turn": map[string]any{"id": turnID, "items": []any{}, "status": "completed"}}})
		default:
			write(map[string]any{"jsonrpc": "2.0", "id": *msg.ID,
				"error": map[string]any{"code": -32601, "message": "method not found: " + msg.Method}})
		}
	}
	// Returning (not os.Exit) keeps go1.24 testing happy: the binary finishes
	// the run and exits on its own once the parent closes stdin.
}

// TestCodexNativeTransportEndToEnd proves the user-invisibility contract: the
// public Runtime/Session API works unchanged when the transport switches to
// the native codex app-server bridge.
func TestCodexNativeTransportEndToEnd(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") == "1" {
		return // avoid re-running inside the helper process
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	agent := CreateCodexNativeAgent(Agent{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestFakeCodexAppServer", "--"},
		Env:     map[string]string{"GO_WANT_HELPER_PROCESS": "1"},
	})
	if agent.Type != CodexNativeRegistryID {
		t.Fatalf("Type = %q, want %q (overrides must not clobber the transport id)", agent.Type, CodexNativeRegistryID)
	}

	runtime := NewRuntime(nil, RuntimeOptions{})
	session, err := runtime.StartSession(ctx, StartSessionOptions{Agent: agent, CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer func() { _ = session.Close(context.Background()) }()

	if sid := session.Snapshot().Session.ID; sid != "fake-thread-1" {
		t.Fatalf("session id = %q, want fake-thread-1 (engine thread id)", sid)
	}

	completion, err := session.Run(ctx, "say FAKE_HELLO")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(completion.OutputText, "FAKE_HELLO") {
		t.Fatalf("OutputText = %q, want FAKE_HELLO (from item/agentMessage/delta)", completion.OutputText)
	}
	if completion.StopReason != "end_turn" {
		t.Fatalf("StopReason = %q, want end_turn", completion.StopReason)
	}
	if completion.Usage == nil {
		t.Fatal("Usage is nil, want rawResponse/completed usage mapped")
	}
	if completion.Usage.InputTokens != 90 || completion.Usage.OutputTokens != 10 || completion.Usage.TotalTokens != 100 {
		t.Fatalf("Usage = %+v, want input=90 output=10 total=100", completion.Usage)
	}

	// Read model parity: the commandExecution item must appear as a tool call
	// with completed status, exactly like the ACP path.
	toolCalls := session.ToolCalls()
	var found *ToolCallSnapshot
	for i := range toolCalls {
		if toolCalls[i].ID == "cmd_1" {
			found = &toolCalls[i]
		}
	}
	if found == nil {
		t.Fatalf("tool call cmd_1 missing from read model; got %+v", toolCalls)
	}
	// Read models are eventually consistent with in-flight updates: poll
	// briefly for the completed status instead of asserting instantly.
	deadline := time.Now().Add(500 * time.Millisecond)
	for found.Status != "completed" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		toolCalls = session.ToolCalls()
		for i := range toolCalls {
			if toolCalls[i].ID == "cmd_1" {
				found = &toolCalls[i]
			}
		}
	}
	if found.Title != "echo hi" || found.Kind != "execute" || found.Status != "completed" {
		t.Fatalf("tool call = %+v, want title=echo hi kind=execute status=completed", found)
	}
}

var pidRe = regexp.MustCompile(`PID=(\d+)`)

// firstPID returns the first fake-process PID marker in the output.
func firstPID(t *testing.T, text string) string {
	t.Helper()
	m := pidRe.FindStringSubmatch(text)
	if m == nil {
		t.Fatalf("no PID marker in %q", text)
	}
	return m[1]
}

// pidFromOutput extracts the fake process marker.
func pidFromOutput(text string) string {
	for _, field := range strings.Fields(text) {
		if strings.HasPrefix(field, "PID=") {
			return strings.TrimPrefix(field, "PID=")
		}
	}
	return ""
}

// TestCodexNativeTurnReuse: turns 2..N on ONE session reuse the SAME
// app-server process (warm RPC + MCP + thread context).
func TestCodexNativeTurnReuse(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") == "1" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	agent := CreateCodexNativeAgent(Agent{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestFakeCodexAppServer", "--"},
		Env:     map[string]string{"GO_WANT_HELPER_PROCESS": "1"},
	})
	runtime := NewRuntime(nil, RuntimeOptions{})
	session, err := runtime.StartSession(ctx, StartSessionOptions{Agent: agent, CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer func() { _ = session.Close(context.Background()) }()

	c1, err := session.Run(ctx, "one")
	if err != nil {
		t.Fatalf("run1: %v", err)
	}
	c2, err := session.Run(ctx, "two")
	if err != nil {
		t.Fatalf("run2: %v", err)
	}
	p1, p2 := firstPID(t, c1.OutputText), firstPID(t, c2.OutputText)
	if p1 == "" || p1 != p2 {
		t.Fatalf("turn 2 ran on a different process: %q vs %q — reuse broken", p2, p1)
	}
}

// TestCodexNativeSessionIsolation: two sessions on the SAME connection get
// TWO separate app-server processes (independent MCP/config).
func TestCodexNativeSessionIsolation(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") == "1" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	agent := CreateCodexNativeAgent(Agent{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestFakeCodexAppServer", "--"},
		Env:     map[string]string{"GO_WANT_HELPER_PROCESS": "1"},
	})
	runtime := NewRuntime(nil, RuntimeOptions{})
	mkSession := func() *Session {
		s, err := runtime.StartSession(ctx, StartSessionOptions{Agent: agent, CWD: t.TempDir()})
		if err != nil {
			t.Fatalf("StartSession: %v", err)
		}
		return s
	}
	s1, s2 := mkSession(), mkSession()
	defer func() { _ = s1.Close(context.Background()); _ = s2.Close(context.Background()) }()

	c1, err := s1.Run(ctx, "one")
	if err != nil {
		t.Fatalf("s1: %v", err)
	}
	c2, err := s2.Run(ctx, "two")
	if err != nil {
		t.Fatalf("s2: %v", err)
	}
	// Same config => shared daemon (pooling); different MCP => own daemon.
	p1, p2 := firstPID(t, c1.OutputText), firstPID(t, c2.OutputText)
	if p1 == "" || p2 == "" || p1 == p2 {
		t.Fatalf("processes not isolated per config: %q vs %q", p1, p2)
	}
}
