package acpruntime

import (
	"encoding/json"
	"testing"
)

// TestClaudeNativePlanMapping: TodoWrite tool_use projects into ACP plan
// entries (like claude-acp), and its tool_result does not create a phantom
// tool call.
func TestClaudeNativePlanMapping(t *testing.T) {
	e := &claudeNativeEngine{}
	e.opts = nativeEngineOptions{Agent: Agent{Command: "claude"}}
	var updates []SessionUpdate
	e.opts.emit = func(sid string, u SessionUpdate) { updates = append(updates, u) }
	proc := &claudeProc{eng: e, acpSessionID: "s1"}
	e.mu.Lock()
	e.procs = map[string]*claudeProc{"s1": proc}
	e.mu.Unlock()

	content := json.RawMessage(`[{"type":"tool_use","id":"todo_1","name":"TodoWrite","input":{"todos":[{"content":"step 1","status":"completed"},{"content":"step 2","status":"pending","priority":"high"}]}}]`)
	proc.handleAssistantBlocks("msg_1", content)

	var plan *SessionUpdate
	var toolCalls int
	for i := range updates {
		switch updates[i].SessionUpdate {
		case "plan":
			plan = &updates[i]
		case "tool_call":
			toolCalls++
		}
	}
	if plan == nil {
		t.Fatalf("no plan update emitted; got %+v", updates)
	}
	if len(plan.Entries) != 2 || plan.Entries[0].Content != "step 1" || plan.Entries[0].Status != "completed" || plan.Entries[1].Priority != "high" {
		t.Fatalf("plan entries = %+v", plan.Entries)
	}
	if toolCalls != 0 {
		t.Fatalf("TodoWrite produced %d tool_call updates, want 0", toolCalls)
	}

	// The later tool_result for the plan tool must be ignored.
	userLine := []byte(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"todo_1"}]}}`)
	proc.handleLine(userLine)
	for _, u := range updates {
		if u.SessionUpdate == "tool_call_update" && u.ToolCallID == "todo_1" {
			t.Fatalf("plan tool produced a tool_call_update: %+v", u)
		}
	}
}

// TestCodexDaemonPooling: same-config sessions share one daemon (fast
// thread/start reuse); different MCP configs get isolated daemons.
func TestCodexDaemonPooling(t *testing.T) {
	e := &codexNativeEngine{}
	e.opts = nativeEngineOptions{Agent: Agent{Command: "codex", Env: map[string]string{
		"CODEX_CONFIG": `{"model_provider":"magpie"}`,
	}}}

	keyA := e.daemonKey(nil, []MCPServer{{Name: "fs", Command: "uvx"}})
	keyA2 := e.daemonKey(nil, []MCPServer{{Name: "fs", Command: "uvx"}})
	if keyA != keyA2 {
		t.Fatalf("identical config produced different keys: %s vs %s", keyA, keyA2)
	}
	keyB := e.daemonKey(nil, []MCPServer{{Name: "db", Command: "other"}})
	if keyB == keyA {
		t.Fatal("different MCP config produced the same daemon key — isolation broken")
	}
	if m := metaString(map[string]any{"model": "x"}, "model"); m != "x" {
		t.Fatalf("metaString = %q", m)
	}
}

// SetSpawnOption after the engine started must fail loudly, not silently
// no-op (model/mode are argv-only on the native path).
