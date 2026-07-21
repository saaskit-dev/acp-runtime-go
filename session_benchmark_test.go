package acpruntime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Session-path benchmarks cover the production hotspots called out in review:
//   - session/update → read model + event emit (with/without consumer backpressure)
//   - finishTurn under a full events buffer (Session.Run does not drain Events)
//   - Snapshot cost after a warm read model
//   - end-to-end turn latency via the simulator agent
//
// Design notes:
//   - Microbenches avoid spawning agents so they isolate library CPU/allocs.
//   - Integration benches use a cached simulator binary and amortize spawn cost
//     outside b.N where possible (one session, many Run iterations).

func BenchmarkEmitTurnEvent_EmptyBuffer(b *testing.B) {
	driver, active := newBenchmarkActiveTurn(64, false)
	// Keep one slot free so the send path succeeds every iteration.
	event := TurnEvent{Type: "text", TurnID: active.id, Text: "x"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Drain one if full so the next emit can succeed; measures successful path.
		select {
		case <-active.events:
		default:
		}
		driver.emitTurnEvent(active, event)
	}
}

func BenchmarkEmitTurnEvent_FullBufferDrop(b *testing.B) {
	driver, active := newBenchmarkActiveTurn(1, false)
	active.events <- TurnEvent{Type: "started", TurnID: active.id}
	event := TurnEvent{Type: "text", TurnID: active.id, Text: "x"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Buffer stays full; emit must not block (P0 backpressure path).
		driver.emitTurnEvent(active, event)
	}
}

func BenchmarkHandleSessionUpdate_TextChunk_BufferDraining(b *testing.B) {
	driver, active := newBenchmarkActiveTurn(64, false)
	stop := startEventDrainer(active.events)
	defer stop()

	notification := textChunkNotification(driver.sessionID, "hello-world-token")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		driver.handleSessionUpdate(notification)
	}
}

func BenchmarkHandleSessionUpdate_TextChunk_BufferNoConsumer(b *testing.B) {
	// Models Session.Run / slow SSE consumers: events fill, then drop.
	driver, _ := newBenchmarkActiveTurn(64, false)
	notification := textChunkNotification(driver.sessionID, "hello-world-token")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		driver.handleSessionUpdate(notification)
	}
}

func BenchmarkHandleSessionUpdate_TextChunk_DropMode(b *testing.B) {
	driver, _ := newBenchmarkActiveTurn(64, true)
	notification := textChunkNotification(driver.sessionID, "hello-world-token")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		driver.handleSessionUpdate(notification)
	}
}

func BenchmarkHandleSessionUpdate_ToolCallUpdate(b *testing.B) {
	driver, active := newBenchmarkActiveTurn(64, false)
	stop := startEventDrainer(active.events)
	defer stop()

	// Seed an existing tool call so update path hits the hot merge branch.
	driver.toolCalls["tool-1"] = ToolCallSnapshot{ID: "tool-1", Title: "Write", Kind: "edit", Status: "pending"}
	driver.operations["tool-1"] = Operation{ID: "tool-1", Kind: "write_file", Phase: "running", Title: "Write"}
	status := "in_progress"
	title := "Write notes"
	notification := SessionNotification{
		SessionID: driver.sessionID,
		Update: SessionUpdate{
			SessionUpdate: "tool_call_update",
			ToolCallID:    "tool-1",
			Title:         &title,
			Status:        &status,
			RawOutput:     []byte(`{"ok":true}`),
		},
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		driver.handleSessionUpdate(notification)
	}
}

func BenchmarkFinishTurn_EventsBufferFull(b *testing.B) {
	// Critical for Session.Run: completion must win even when events is saturated.
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		driver, active := newBenchmarkActiveTurn(1, false)
		active.events <- TurnEvent{Type: "text", TurnID: active.id, Text: "fill"}
		b.StartTimer()
		driver.finishTurn(active, TurnCompletion{TurnID: active.id, OutputText: "done"}, nil)
		b.StopTimer()
		// Drain completion so the channel is not leaked into the next iteration.
		<-active.completion
	}
}

func BenchmarkSnapshot_AfterWarmReadModel(b *testing.B) {
	driver, active := newBenchmarkActiveTurn(64, true)
	// Warm thread / toolCalls / permissions to approximate a multi-turn session.
	for i := 0; i < 64; i++ {
		driver.thread = append(driver.thread, ThreadEntry{
			ID:     fmt.Sprintf("msg-%d", i),
			Kind:   "assistant_message",
			Status: "completed",
			Text:   "warm text payload for snapshot copy cost",
		})
		id := fmt.Sprintf("tool-%d", i)
		driver.toolCalls[id] = ToolCallSnapshot{ID: id, Title: "op", Kind: "edit", Status: "completed"}
		driver.operations[id] = Operation{ID: id, Kind: "write_file", Phase: "completed", Title: "op"}
	}
	_ = active
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = driver.Snapshot()
	}
}

func BenchmarkThreadEntries_AfterWarmReadModel(b *testing.B) {
	driver, _ := newBenchmarkActiveTurn(64, true)
	for i := 0; i < 256; i++ {
		driver.thread = append(driver.thread, ThreadEntry{
			ID:     fmt.Sprintf("msg-%d", i),
			Kind:   "user_message",
			Status: "completed",
			Text:   "entry",
		})
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = driver.ThreadEntries()
	}
}

func BenchmarkSessionRun_Simulator_ShortPrompt(b *testing.B) {
	session := newBenchmarkSimulatorSession(b)
	defer session.Close(context.Background())
	ctx := context.Background()

	// Prime agent/process so b.N measures turn cost, not first-spawn noise.
	if _, err := session.Run(ctx, "Reply with the single word OK."); err != nil {
		b.Fatalf("prime Run() error = %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := session.Run(ctx, "Reply with the single word OK."); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSessionStartTurn_Simulator_DrainEvents(b *testing.B) {
	session := newBenchmarkSimulatorSession(b)
	defer session.Close(context.Background())
	ctx := context.Background()

	if _, err := session.Run(ctx, "Reply with the single word OK."); err != nil {
		b.Fatalf("prime Run() error = %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		handle := session.StartTurn(ctx, RuntimePrompt{Text: "Reply with the single word OK."})
		for range handle.Events {
		}
		result := <-handle.Completion
		if result.Err != nil {
			b.Fatal(result.Err)
		}
	}
}

func BenchmarkSessionRun_Simulator_ParallelSessions(b *testing.B) {
	// Each parallel worker owns an independent session/process to measure
	// multi-session host throughput without tripping single-flight StartTurn.
	bin := cachedSimulatorBinary(b)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		ctx := context.Background()
		cwd, err := os.MkdirTemp("", "acp-bench-cwd-*")
		if err != nil {
			b.Fatal(err)
		}
		defer os.RemoveAll(cwd)
		storage, err := os.MkdirTemp("", "acp-bench-storage-*")
		if err != nil {
			b.Fatal(err)
		}
		defer os.RemoveAll(storage)

		runtime := NewRuntime(NewStdioConnectionFactory(StdioFactoryOptions{}), RuntimeOptions{})
		session, err := runtime.StartSession(ctx, StartSessionOptions{
			Agent: Agent{
				Type:    LocalSimulatorAgentACPRegistryID,
				Command: bin,
				Args:    []string{"--auth-mode", "none", "--storage-dir", storage},
			},
			CWD: cwd,
		})
		if err != nil {
			b.Fatal(err)
		}
		defer session.Close(context.Background())

		for pb.Next() {
			if _, err := session.Run(ctx, "Reply with the single word OK."); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func newBenchmarkActiveTurn(buffer int, drop bool) (*acpSessionDriver, *activeTurn) {
	driver := &acpSessionDriver{
		sessionID:   "bench-session",
		status:      "running",
		toolCalls:   map[string]ToolCallSnapshot{},
		operations:  map[string]Operation{},
		permissions: map[string]PermissionRequestSnapshot{},
		rawConfig:   map[string]any{"mode": "accept-edits"},
		profile:     defaultAgentProfile(),
		metadata: RuntimeSessionMetadata{
			SessionID:     "bench-session",
			CurrentModeID: "accept-edits",
		},
		mcpServers: []MCPServer{},
		agent:      Agent{Type: LocalSimulatorAgentACPRegistryID, Command: "noop"},
		cwd:        "/tmp",
	}
	active := &activeTurn{
		id:               "turn-1",
		events:           make(chan TurnEvent, buffer),
		completion:       make(chan TurnResult, 1),
		dropIntermediate: drop,
	}
	driver.currentTurn = active
	driver.turnSeq = 1
	return driver, active
}

func textChunkNotification(sessionID, text string) SessionNotification {
	return SessionNotification{
		SessionID: sessionID,
		Update: SessionUpdate{
			SessionUpdate: "agent_message_chunk",
			Text:          text,
		},
	}
}

func startEventDrainer(events <-chan TurnEvent) (stop func()) {
	done := make(chan struct{})
	var once sync.Once
	go func() {
		for {
			select {
			case <-done:
				return
			case _, ok := <-events:
				if !ok {
					return
				}
			}
		}
	}()
	return func() {
		once.Do(func() { close(done) })
	}
}

var (
	simulatorBuildOnce sync.Once
	simulatorBuildPath string
	simulatorBuildErr  error
)

func cachedSimulatorBinary(tb testing.TB) string {
	tb.Helper()
	simulatorBuildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "acp-bench-sim-*")
		if err != nil {
			simulatorBuildErr = err
			return
		}
		bin := filepath.Join(dir, "acp-simulator-agent")
		cmd := exec.Command("go", "build", "-o", bin, "./cmd/acp-simulator-agent")
		if output, err := cmd.CombinedOutput(); err != nil {
			simulatorBuildErr = fmt.Errorf("go build simulator: %w\n%s", err, output)
			return
		}
		simulatorBuildPath = bin
	})
	if simulatorBuildErr != nil {
		tb.Fatal(simulatorBuildErr)
	}
	return simulatorBuildPath
}

func newBenchmarkSimulatorSession(b *testing.B) *Session {
	b.Helper()
	bin := cachedSimulatorBinary(b)
	cwd := b.TempDir()
	storage := b.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	b.Cleanup(cancel)

	runtime := NewRuntime(NewStdioConnectionFactory(StdioFactoryOptions{}), RuntimeOptions{})
	session, err := runtime.StartSession(ctx, StartSessionOptions{
		Agent: Agent{
			Type:    LocalSimulatorAgentACPRegistryID,
			Command: bin,
			Args:    []string{"--auth-mode", "none", "--storage-dir", storage},
		},
		CWD: cwd,
	})
	if err != nil {
		b.Fatalf("StartSession() error = %v", err)
	}
	return session
}
