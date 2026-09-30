package acpruntime

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Fixed source: openai/codex rust-v0.153.4,
// app-server-protocol/src/protocol/v2/turn.rs and protocol/common.rs.
// These fixtures are deliberately literal native protocol JSON, independent
// of the Go ACP DTOs used by the runtime.
func TestNativeCodexUniqueTerminalAndStaleIDs(t *testing.T) {
	for _, status := range []string{"failed", "interrupted", "completed", "unknown", "inProgress"} {
		t.Run(status, func(t *testing.T) {
			turn := &codexTurn{id: "real", done: make(chan struct{}), deltas: map[string]bool{}}
			daemon := &codexDaemon{turns: map[string]*codexTurn{"thread": turn}, peer: NewPeer(strings.NewReader(""), io.Discard, PeerOptions{}), eng: &codexNativeEngine{opts: nativeEngineOptions{emit: func(string, SessionUpdate) {}}}}
			defer daemon.peer.Close()
			daemon.registerHandlers()
			handler := daemon.peer.notificationHandlers["turn/completed"]
			handler(context.Background(), json.RawMessage(`{"threadId":"thread","turn":{"id":"old","items":[],"status":"completed"}}`))
			if turn.finished {
				t.Fatal("stale terminal completed current turn")
			}
			handler(context.Background(), json.RawMessage(`{"threadId":"thread","turn":{"id":"real","items":[],"status":"`+status+`","error":{"message":"provider failure","codexErrorInfo":"other","additionalDetails":"detail"}}}`))
			if !turn.finished {
				t.Fatal("terminal not settled")
			}
			if (turn.err == nil) != (status == "completed") {
				t.Fatalf("status %s err %v", status, turn.err)
			}
			if status == "interrupted" && !errors.Is(turn.err, context.Canceled) {
				t.Fatal("interrupted not cancellation")
			}
			original := turn.err
			handler(context.Background(), json.RawMessage(`{"threadId":"thread","turn":{"id":"real","items":[],"status":"completed"}}`))
			if turn.err != original {
				t.Fatal("duplicate changed terminal outcome")
			}
		})
	}
}
func TestNativeCodexErrorNotificationWaitsForTerminal(t *testing.T) {
	turn := &codexTurn{id: "real", done: make(chan struct{}), deltas: map[string]bool{}}
	daemon := &codexDaemon{turns: map[string]*codexTurn{"thread": turn}, peer: NewPeer(strings.NewReader(""), io.Discard, PeerOptions{})}
	defer daemon.peer.Close()
	daemon.registerHandlers()
	daemon.peer.notificationHandlers["error"](context.Background(), json.RawMessage(`{"threadId":"thread","turnId":"real","willRetry":false,"error":{"message":"failed"}}`))
	if turn.finished || turn.err == nil {
		t.Fatalf("error is not terminal confirmation: finished=%v error=%v", turn.finished, turn.err)
	}
	daemon.peer.notificationHandlers["turn/completed"](context.Background(), json.RawMessage(`{"threadId":"thread","turn":{"id":"real","items":[],"status":"failed","error":null}}`))
	if !turn.finished || turn.err == nil {
		t.Fatal("failure lost")
	}
}
func TestNativeCodexCancelBeforeIDAndAckKeepsBusy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	clientIn, serverOut := io.Pipe()
	serverIn, clientOut := io.Pipe()
	client := NewPeer(clientIn, clientOut, PeerOptions{})
	server := NewPeer(serverIn, serverOut, PeerOptions{})
	defer client.Close()
	defer server.Close()
	defer clientIn.Close()
	defer clientOut.Close()
	defer serverIn.Close()
	defer serverOut.Close()
	started, release := make(chan struct{}), make(chan struct{})
	interrupt := make(chan json.RawMessage, 1)
	server.RegisterRequest("turn/start", func(context.Context, json.RawMessage) (any, error) {
		close(started)
		<-release
		return json.RawMessage(`{"turn":{"id":"real-turn-review","items":[],"status":"inProgress"}}`), nil
	})
	server.RegisterRequest("turn/interrupt", func(_ context.Context, raw json.RawMessage) (any, error) {
		interrupt <- append(json.RawMessage(nil), raw...)
		return json.RawMessage(`{}`), nil
	})
	go client.Start(ctx)
	go server.Start(ctx)
	daemon := &codexDaemon{peer: client, turns: map[string]*codexTurn{}, eng: &codexNativeEngine{opts: nativeEngineOptions{emit: func(string, SessionUpdate) {}}}}
	daemon.registerHandlers()
	result := make(chan error, 1)
	go func() {
		_, err := daemon.prompt(ctx, "thread", []ContentBlock{{Type: "text", Text: "hello"}})
		result <- err
	}()
	<-started
	daemon.interrupt("thread")
	close(release)
	select {
	case raw := <-interrupt:
		if !strings.Contains(string(raw), `"turnId":"real-turn-review"`) {
			t.Fatalf("interrupt=%s", raw)
		}
	case <-ctx.Done():
		t.Fatal("missing interrupt request")
	}
	if _, err := daemon.prompt(ctx, "thread", nil); err == nil {
		t.Fatal("interrupt ack released active turn")
	}
	if err := server.Notify(ctx, "turn/completed", json.RawMessage(`{"threadId":"thread","turn":{"id":"old","items":[],"status":"completed"}}`)); err != nil {
		t.Fatal(err)
	}
	if err := server.Notify(ctx, "turn/completed", json.RawMessage(`{"threadId":"thread","turn":{"id":"real-turn-review","items":[],"status":"interrupted"}}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("result=%v", err)
		}
	case <-ctx.Done():
		t.Fatal("missing terminal")
	}
}

type nativePermissionCapture struct {
	mu     sync.Mutex
	lines  [][]byte
	notify chan struct{}
}

func (w *nativePermissionCapture) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.lines = append(w.lines, append([]byte(nil), p...))
	w.mu.Unlock()
	select {
	case w.notify <- struct{}{}:
	default:
	}
	return len(p), nil
}
func (*nativePermissionCapture) Close() error { return nil }

func TestNativeClaudeApprovalAsyncContextAndCancellation(t *testing.T) {
	entered := make(chan PermissionRequest, 1)
	cancelled := make(chan struct{})
	capture := &nativePermissionCapture{notify: make(chan struct{}, 4)}
	permissionCtx, cancelPermission := context.WithCancel(context.Background())
	defer cancelPermission()
	proc := &claudeProc{acpSessionID: "session", stdin: capture, turn: &claudeTurn{done: make(chan struct{}), permissionCtx: permissionCtx, cancelPermission: cancelPermission}}
	proc.eng = &claudeNativeEngine{opts: nativeEngineOptions{requestPermission: func(ctx context.Context, req PermissionRequest) (PermissionDecision, error) {
		entered <- req
		<-ctx.Done()
		close(cancelled)
		return PermissionDecision{Outcome: "selected", OptionID: "allow"}, nil
	}}}
	line := []byte(`{"type":"control_request","request_id":"approval1","request":{"subtype":"can_use_tool","tool_name":"Bash","tool_use_id":"tool1","input":{"command":"cat /private/file","cwd":"/private"},"decision_reason":"sandbox"}}`)
	returned := make(chan struct{})
	go func() { proc.handleLine(line); close(returned) }()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("approval blocked native reader")
	}
	var req PermissionRequest
	select {
	case req = <-entered:
	case <-time.After(time.Second):
		t.Fatal("authority not called")
	}
	if req.ToolCallID != "tool1" || req.Kind != "execute" || !strings.Contains(string(req.RawInput), "/private/file") || req.Meta["x-acp-runtime-native-request"] == nil {
		t.Fatalf("incomplete context=%+v", req)
	}
	proc.cancel()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("cancel did not cancel approval")
	}
	deadline := time.After(time.Second)
	for {
		capture.mu.Lock()
		joined := string(strings.Join(func() []string {
			var result []string
			for _, line := range capture.lines {
				result = append(result, string(line))
			}
			return result
		}(), "\n"))
		capture.mu.Unlock()
		if strings.Contains(joined, `"behavior":"allow"`) {
			t.Fatal("late allow escaped cancelled turn")
		}
		if strings.Contains(joined, `"behavior":"deny"`) {
			break
		}
		select {
		case <-capture.notify:
		case <-deadline:
			t.Fatal("no denial after cancel")
		}
	}
}

func TestNativeClaudeApprovalFailureAndUnknownToolDeny(t *testing.T) {
	for _, tool := range []string{"Bash", "UnknownDangerousTool"} {
		t.Run(tool, func(t *testing.T) {
			capture := &nativePermissionCapture{notify: make(chan struct{}, 1)}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			proc := &claudeProc{acpSessionID: "session", stdin: capture, turn: &claudeTurn{done: make(chan struct{}), permissionCtx: ctx, cancelPermission: cancel}}
			proc.eng = &claudeNativeEngine{opts: nativeEngineOptions{requestPermission: func(context.Context, PermissionRequest) (PermissionDecision, error) {
				calls++
				return PermissionDecision{Outcome: "selected", OptionID: "allow"}, errors.New("authority failed")
			}}}
			proc.handleLine([]byte(`{"type":"control_request","request_id":"one","request":{"subtype":"can_use_tool","tool_name":"` + tool + `","input":{}}}`))
			select {
			case <-capture.notify:
			case <-time.After(time.Second):
				t.Fatal("missing permission reply")
			}
			capture.mu.Lock()
			lines := string(capture.lines[0])
			capture.mu.Unlock()
			if !strings.Contains(lines, `"behavior":"deny"`) {
				t.Fatalf("reply=%s", lines)
			}
			if tool == "UnknownDangerousTool" && calls != 0 {
				t.Fatal("unknown tool forwarded for approval")
			}
		})
	}
}

func TestNativeClaudePermissionEOFHelper(t *testing.T) {
	marker := os.Getenv("ACP_PERMISSION_EOF_MARKER")
	if marker == "" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		return
	}
	fmt.Fprintln(os.Stdout, `{"type":"control_request","request_id":"eof-approval","request":{"subtype":"can_use_tool","tool_name":"Read","tool_use_id":"tool-eof","input":{"file_path":"/some/file"}}}`)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			return
		}
		time.Sleep(time.Millisecond)
	}
}
func TestNativeClaudeEOFCancelsPendingApproval(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "authority-entered")
	cancelled := make(chan struct{})
	agent := CreateClaudeCodeNativeAgent(Agent{Command: os.Args[0], Args: []string{"-test.run=^TestNativeClaudePermissionEOFHelper$", "--"}, Env: map[string]string{"ACP_PERMISSION_EOF_MARKER": marker}})
	opts := nativeEngineOptions{Agent: agent, CWD: t.TempDir(), requestPermission: func(ctx context.Context, request PermissionRequest) (PermissionDecision, error) {
		if err := os.WriteFile(marker, []byte("ready"), 0600); err != nil {
			return PermissionDecision{}, err
		}
		<-ctx.Done()
		close(cancelled)
		return PermissionDecision{Outcome: "selected", OptionID: "allow"}, nil
	}}
	engine := &claudeNativeEngine{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := engine.Start(ctx, opts); err != nil {
		t.Fatal(err)
	}
	defer engine.Close(context.Background())
	id, err := engine.NewSession(ctx, opts, NewSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Prompt(ctx, opts, id, []ContentBlock{{Type: "text", Text: "read"}}); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("EOF result=%v", err)
	}
	select {
	case <-cancelled:
	case <-ctx.Done():
		t.Fatal("EOF did not cancel pending authority")
	}
}
func TestNativeClaudeDuplicateApprovalCannotReleaseLateAllow(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	capture := &nativePermissionCapture{notify: make(chan struct{}, 4)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	proc := &claudeProc{acpSessionID: "session", stdin: capture, turn: &claudeTurn{done: make(chan struct{}), permissionCtx: ctx, cancelPermission: cancel}}
	proc.eng = &claudeNativeEngine{opts: nativeEngineOptions{requestPermission: func(context.Context, PermissionRequest) (PermissionDecision, error) {
		close(entered)
		<-release
		return PermissionDecision{Outcome: "selected", OptionID: "allow"}, nil
	}}}
	raw := []byte(`{"type":"control_request","request_id":"duplicate","request":{"subtype":"can_use_tool","tool_name":"Read","input":{"file_path":"/file"}}}`)
	proc.handleLine(raw)
	<-entered
	proc.handleLine(raw)
	close(release)
	deadline := time.After(time.Second)
	for {
		capture.mu.Lock()
		count := len(capture.lines)
		for _, line := range capture.lines {
			if strings.Contains(string(line), `"behavior":"allow"`) {
				t.Fatal("duplicate permission ID received allow")
			}
		}
		capture.mu.Unlock()
		if count >= 2 {
			break
		}
		select {
		case <-capture.notify:
		case <-deadline:
			t.Fatal("missing duplicate denials")
		}
	}
}

func TestNativeConfigResponseIncludesFullSnapshot(t *testing.T) {
	engine := &codexNativeEngine{sessions: map[string]*codexSession{"session": {model: "chosen"}}}
	peer := NewPeer(strings.NewReader(""), io.Discard, PeerOptions{})
	defer peer.Close()
	bridge := &nativeBridge{engine: engine, peer: peer}
	registerNativeBridgeHandlers(bridge)
	result, err := peer.requestHandlers["session/set_config_option"](context.Background(), json.RawMessage(`{"sessionId":"session","configId":"model","value":"chosen","type":"select"}`))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	options, ok := wire["configOptions"].([]any)
	if !ok || len(options) != 1 {
		t.Fatalf("required configOptions absent: %s", encoded)
	}
	option := options[0].(map[string]any)
	if option["type"] != "select" || option["currentValue"] != "chosen" {
		t.Fatalf("config readback=%s", encoded)
	}
}
