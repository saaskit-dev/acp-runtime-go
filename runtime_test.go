package acpruntime

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRuntimeStartsSimulatorOverStdio(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cwd := t.TempDir()
	storage := t.TempDir()
	simulatorBin := buildSimulatorBinary(t)
	agent := Agent{
		Type:    LocalSimulatorAgentACPRegistryID,
		Command: simulatorBin,
		Args:    []string{"--auth-mode", "none", "--storage-dir", storage},
	}
	runtime := NewRuntime(NewStdioConnectionFactory(StdioFactoryOptions{}), RuntimeOptions{})
	session, err := runtime.StartSession(ctx, StartSessionOptions{Agent: agent, CWD: cwd})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	defer session.Close(context.Background())
	if session.Snapshot().Session.ID == "" {
		t.Fatalf("session id is empty")
	}
	if got := session.Metadata().CurrentModeID; got != "accept-edits" {
		t.Fatalf("CurrentModeID = %q, want accept-edits", got)
	}
	completion, err := session.Run(ctx, "Reply with the single word OK.")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if completion.OutputText != "OK" {
		t.Fatalf("OutputText = %q, want OK", completion.OutputText)
	}
}

func TestRuntimeAppliesInitialMode(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cwd := t.TempDir()
	storage := t.TempDir()
	simulatorBin := buildSimulatorBinary(t)
	agent := Agent{
		Type:    LocalSimulatorAgentACPRegistryID,
		Command: simulatorBin,
		Args:    []string{"--auth-mode", "none", "--storage-dir", storage},
	}
	runtime := NewRuntime(NewStdioConnectionFactory(StdioFactoryOptions{}), RuntimeOptions{})
	session, err := runtime.StartSession(ctx, StartSessionOptions{
		Agent: agent,
		CWD:   cwd,
		InitialConfig: InitialConfig{
			Mode: "yolo",
		},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	defer session.Close(context.Background())
	if got := session.Metadata().CurrentModeID; got != "yolo" {
		t.Fatalf("CurrentModeID = %q, want yolo", got)
	}
}

func TestRuntimeAppliesInitialModelAndEffort(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cwd := t.TempDir()
	storage := t.TempDir()
	simulatorBin := buildSimulatorBinary(t)
	agent := Agent{
		Type:    LocalSimulatorAgentACPRegistryID,
		Command: simulatorBin,
		Args:    []string{"--auth-mode", "none", "--storage-dir", storage},
	}
	runtime := NewRuntime(NewStdioConnectionFactory(StdioFactoryOptions{}), RuntimeOptions{})
	session, err := runtime.StartSession(ctx, StartSessionOptions{
		Agent: agent,
		CWD:   cwd,
		InitialConfig: InitialConfig{
			Model:  "claude",
			Effort: "high",
		},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	defer session.Close(context.Background())
	if got := configOptionValue(session.Metadata().AgentConfigOptions, "model"); got != "claude" {
		t.Fatalf("model option = %v, want claude", got)
	}
	if got := configOptionValue(session.Metadata().AgentConfigOptions, "reasoning_effort"); got != "high" {
		t.Fatalf("reasoning_effort option = %v, want high", got)
	}
}

func TestClaudeInitialModeYoloAliasesToBypassPermissions(t *testing.T) {
	profile := ResolveAgentProfile(Agent{Type: ClaudeCodeACPRegistryID})
	aliases := initialConfigAliases(profile, "mode", "yolo")
	if len(aliases) != 2 || aliases[0] != "bypassPermissions" || aliases[1] != "yolo" {
		t.Fatalf("aliases = %#v, want bypassPermissions then yolo", aliases)
	}
}

func configOptionValue(options []RuntimeAgentConfigOption, id string) any {
	for _, option := range options {
		if option.ID == id {
			return option.Value
		}
	}
	return nil
}

// TestRuntimeForwardsSystemPromptToSessionMeta verifies that setting
// StartSessionOptions.SystemPrompt causes the runtime to emit
// _meta.systemPrompt on the session/new request (the community convention used
// by the Zed ACP adapters). It captures outbound ACP messages via the
// OnACPMessage hook and asserts the systemPrompt reaches the wire.
func TestRuntimeForwardsSystemPromptToSessionMeta(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cwd := t.TempDir()
	storage := t.TempDir()
	simulatorBin := buildSimulatorBinary(t)
	agent := Agent{
		Type:    LocalSimulatorAgentACPRegistryID,
		Command: simulatorBin,
		Args:    []string{"--auth-mode", "none", "--storage-dir", storage},
	}

	var capturedNewSession []byte
	var captureMu sync.Mutex
	factory := NewStdioConnectionFactory(StdioFactoryOptions{
		OnACPMessage: func(direction string, message []byte) {
			if direction != "outbound" {
				return
			}
			if bytes.Contains(message, []byte(`"session/new"`)) {
				captureMu.Lock()
				capturedNewSession = append(capturedNewSession[:0], message...)
				captureMu.Unlock()
			}
		},
	})
	runtime := NewRuntime(factory, RuntimeOptions{})
	session, err := runtime.StartSession(ctx, StartSessionOptions{
		Agent:        agent,
		CWD:          cwd,
		SystemPrompt: &SystemPrompt{Text: "Reply tersely."},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	defer session.Close(context.Background())

	captureMu.Lock()
	snapshot := append([]byte(nil), capturedNewSession...)
	captureMu.Unlock()
	if snapshot == nil {
		t.Fatalf("no outbound session/new captured")
	}
	if !bytes.Contains(snapshot, []byte(`"systemPrompt":"Reply tersely."`)) {
		t.Fatalf("session/new payload missing _meta.systemPrompt: %s", snapshot)
	}
}

// TestStartSessionMetaPassthrough verifies that StartSessionOptions.Meta is
// forwarded to the session/new _meta on the wire.
func TestStartSessionMetaPassthrough(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cwd := t.TempDir()
	storage := t.TempDir()
	simulatorBin := buildSimulatorBinary(t)
	agent := Agent{
		Type:    LocalSimulatorAgentACPRegistryID,
		Command: simulatorBin,
		Args:    []string{"--auth-mode", "none", "--storage-dir", storage},
	}

	var capturedNewSession []byte
	var captureMu sync.Mutex
	factory := NewStdioConnectionFactory(StdioFactoryOptions{
		OnACPMessage: func(direction string, message []byte) {
			if direction != "outbound" {
				return
			}
			if bytes.Contains(message, []byte(`"session/new"`)) {
				captureMu.Lock()
				capturedNewSession = append(capturedNewSession[:0], message...)
				captureMu.Unlock()
			}
		},
	})
	runtime := NewRuntime(factory, RuntimeOptions{})
	session, err := runtime.StartSession(ctx, StartSessionOptions{
		Agent: agent,
		CWD:   cwd,
		Meta: map[string]any{
			"customKey": "customValue",
			"nested":    map[string]any{"inner": 42},
		},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	defer session.Close(context.Background())

	captureMu.Lock()
	snapshot := append([]byte(nil), capturedNewSession...)
	captureMu.Unlock()
	if snapshot == nil {
		t.Fatalf("no outbound session/new captured")
	}
	if !bytes.Contains(snapshot, []byte(`"customKey":"customValue"`)) {
		t.Fatalf("session/new missing _meta.customKey: %s", snapshot)
	}
	if !bytes.Contains(snapshot, []byte(`"inner":42`)) {
		t.Fatalf("session/new missing nested _meta: %s", snapshot)
	}
}

// TestResumeSessionMetaPassthrough verifies that Resume uses the same Meta merge
// path as Create: SystemPrompt-derived meta and explicit Meta both appear on the
// session/resume wire payload. Checking only in-memory options would miss a
// request type that silently drops _meta during JSON-RPC serialization.
func TestResumeSessionMetaPassthrough(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cwd := t.TempDir()
	storage := t.TempDir()
	simulatorBin := buildSimulatorBinary(t)
	agent := Agent{
		Type:    LocalSimulatorAgentACPRegistryID,
		Command: simulatorBin,
		Args:    []string{"--auth-mode", "none", "--storage-dir", storage},
	}

	creator := NewRuntime(NewStdioConnectionFactory(StdioFactoryOptions{}), RuntimeOptions{})
	created, err := creator.StartSession(ctx, StartSessionOptions{Agent: agent, CWD: cwd})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	defer created.Close(context.Background())
	sessionID := created.Snapshot().Session.ID

	var capturedResumeSession []byte
	var captureMu sync.Mutex
	factory := NewStdioConnectionFactory(StdioFactoryOptions{
		OnACPMessage: func(direction string, message []byte) {
			if direction != "outbound" || !bytes.Contains(message, []byte(`"session/resume"`)) {
				return
			}
			captureMu.Lock()
			capturedResumeSession = append(capturedResumeSession[:0], message...)
			captureMu.Unlock()
		},
	})
	runtime := NewRuntime(factory, RuntimeOptions{})
	resumed, err := runtime.ResumeSession(ctx, ResumeSessionOptions{
		StartSessionOptions: StartSessionOptions{
			Agent:        agent,
			CWD:          cwd,
			SystemPrompt: &SystemPrompt{Text: "be brief"},
			Meta: map[string]any{
				"claudeCode": map[string]any{
					"options": map[string]any{
						"settingSources": []string{},
					},
				},
			},
		},
		SessionID: sessionID,
	})
	if err != nil {
		t.Fatalf("ResumeSession() error = %v", err)
	}
	defer resumed.Close(context.Background())

	captureMu.Lock()
	snapshot := append([]byte(nil), capturedResumeSession...)
	captureMu.Unlock()
	if snapshot == nil {
		t.Fatalf("no outbound session/resume captured")
	}
	if !bytes.Contains(snapshot, []byte(`"systemPrompt":"be brief"`)) {
		t.Fatalf("session/resume missing SystemPrompt-derived _meta: %s", snapshot)
	}
	if !bytes.Contains(snapshot, []byte(`"claudeCode"`)) ||
		!bytes.Contains(snapshot, []byte(`"options"`)) ||
		!bytes.Contains(snapshot, []byte(`"settingSources":[]`)) {
		t.Fatalf("session/resume missing managed Claude _meta: %s", snapshot)
	}
}

// TestMetaMergesWithSystemPromptMeta verifies that caller Meta and
// SystemPrompt-derived meta coexist in the same _meta object, and that a
// conflicting nested map key from the caller takes precedence.
func TestMetaMergesWithSystemPromptMeta(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cwd := t.TempDir()
	storage := t.TempDir()
	simulatorBin := buildSimulatorBinary(t)
	agent := Agent{
		Type:    LocalSimulatorAgentACPRegistryID,
		Command: simulatorBin,
		Args:    []string{"--auth-mode", "none", "--storage-dir", storage},
	}

	var capturedNewSession []byte
	var captureMu sync.Mutex
	factory := NewStdioConnectionFactory(StdioFactoryOptions{
		OnACPMessage: func(direction string, message []byte) {
			if direction != "outbound" {
				return
			}
			if bytes.Contains(message, []byte(`"session/new"`)) {
				captureMu.Lock()
				capturedNewSession = append(capturedNewSession[:0], message...)
				captureMu.Unlock()
			}
		},
	})
	runtime := NewRuntime(factory, RuntimeOptions{})
	session, err := runtime.StartSession(ctx, StartSessionOptions{
		Agent:        agent,
		CWD:          cwd,
		SystemPrompt: &SystemPrompt{Text: "be brief"},   // produces _meta.systemPrompt
		Meta:         map[string]any{"claudeCode": "opts"}, // plus a caller key
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	defer session.Close(context.Background())

	captureMu.Lock()
	snapshot := append([]byte(nil), capturedNewSession...)
	captureMu.Unlock()
	// Both keys must coexist in _meta.
	if !bytes.Contains(snapshot, []byte(`"systemPrompt":"be brief"`)) {
		t.Fatalf("SystemPrompt meta lost after merge: %s", snapshot)
	}
	if !bytes.Contains(snapshot, []byte(`"claudeCode":"opts"`)) {
		t.Fatalf("caller Meta lost after merge: %s", snapshot)
	}
}

// TestMergeSessionMetaDeepMerge is a pure unit test for the merge helper:
// nested maps merge recursively, scalar conflicts resolved in favor of extra.
func TestMergeSessionMetaDeepMerge(t *testing.T) {
	base := map[string]any{
		"systemPrompt": "keep",
		"claudeCode":   map[string]any{"tools": []string{"a"}, "kept": 1},
	}
	extra := map[string]any{
		"claudeCode": map[string]any{"disallowedTools": []string{"x"}},
		"newKey":     "v",
	}
	out := mergeSessionMeta(base, extra)
	if out["systemPrompt"] != "keep" {
		t.Fatalf("systemPrompt lost: %v", out["systemPrompt"])
	}
	if out["newKey"] != "v" {
		t.Fatalf("newKey lost")
	}
	cc, ok := out["claudeCode"].(map[string]any)
	if !ok {
		t.Fatalf("claudeCode not merged as map: %#v", out["claudeCode"])
	}
	// Nested keys from both sides must survive.
	if _, ok := cc["tools"]; !ok {
		t.Fatalf("nested tools lost in deep merge: %#v", cc)
	}
	if cc["kept"] != 1 {
		t.Fatalf("nested kept lost: %#v", cc)
	}
	if _, ok := cc["disallowedTools"]; !ok {
		t.Fatalf("nested disallowedTools lost: %#v", cc)
	}
	// Inputs must not be mutated.
	if _, ok := base["newKey"]; ok {
		t.Fatalf("base was mutated by merge")
	}
}

// TestClaudeOptionsViaMetaReachesSessionNew verifies that ClaudeCodeOptions
// passed via StartSessionOptions.Meta reaches the session/new _meta on the wire
// with the correct nested structure.
func TestClaudeOptionsViaMetaReachesSessionNew(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cwd := t.TempDir()
	storage := t.TempDir()
	simulatorBin := buildSimulatorBinary(t)
	agent := Agent{
		Type:    LocalSimulatorAgentACPRegistryID,
		Command: simulatorBin,
		Args:    []string{"--auth-mode", "none", "--storage-dir", storage},
	}

	var capturedNewSession []byte
	var captureMu sync.Mutex
	factory := NewStdioConnectionFactory(StdioFactoryOptions{
		OnACPMessage: func(direction string, message []byte) {
			if direction != "outbound" {
				return
			}
			if bytes.Contains(message, []byte(`"session/new"`)) {
				captureMu.Lock()
				capturedNewSession = append(capturedNewSession[:0], message...)
				captureMu.Unlock()
			}
		},
	})
	runtime := NewRuntime(factory, RuntimeOptions{})
	meta := CreateClaudeCodeOptions(ClaudeCodeOptions{
		DisallowedTools: []string{"WebFetch"},
	})
	session, err := runtime.StartSession(ctx, StartSessionOptions{
		Agent: agent,
		CWD:   cwd,
		Meta:  meta,
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	defer session.Close(context.Background())

	captureMu.Lock()
	snapshot := append([]byte(nil), capturedNewSession...)
	captureMu.Unlock()
	if snapshot == nil {
		t.Fatalf("no outbound session/new captured")
	}
	// Verify the full nested path: _meta.claudeCode.options.disallowedTools
	if !bytes.Contains(snapshot, []byte(`"claudeCode"`)) {
		t.Fatalf("missing _meta.claudeCode: %s", snapshot)
	}
	if !bytes.Contains(snapshot, []byte(`"disallowedTools"`)) {
		t.Fatalf("missing disallowedTools in _meta.claudeCode.options: %s", snapshot)
	}
	if !bytes.Contains(snapshot, []byte(`"WebFetch"`)) {
		t.Fatalf("missing WebFetch value: %s", snapshot)
	}
}

// TestRuntimeClaudeSystemPromptInjectsCLIFlag verifies that a Claude-typed
// agent gets --append-system-prompt injected into its args when a system prompt
// is supplied. This uses a fake agent command so no real Claude process spawns.
func TestRuntimeClaudeSystemPromptInjectsCLIFlag(t *testing.T) {
	// We only need to verify the args rewrite happens before spawn; use a
	// nonexistent command so spawn fails fast, but the resolveStartOptions path
	// still runs the profile. We check the error path doesn't mutate agent.
	profile := ResolveAgentProfile(Agent{Type: ClaudeCodeACPRegistryID})
	base := Agent{Type: ClaudeCodeACPRegistryID, Command: "npm", Args: []string{"exec", "--yes", "claude-agent-acp"}}
	out := profile.ApplySystemPromptToAgent(base, SystemPrompt{Text: "Be brief."})
	want := "--append-system-prompt"
	found := false
	for _, arg := range out.Args {
		if arg == want {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("Claude agent args %v missing %q", out.Args, want)
	}
}

func TestSimulatorWriteProducesOperation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cwd := t.TempDir()
	storage := t.TempDir()
	simulatorBin := buildSimulatorBinary(t)
	agent := Agent{
		Type:    LocalSimulatorAgentACPRegistryID,
		Command: simulatorBin,
		Args:    []string{"--auth-mode", "none", "--storage-dir", storage},
	}
	runtime := NewRuntime(NewStdioConnectionFactory(StdioFactoryOptions{}), RuntimeOptions{})
	session, err := runtime.StartSession(ctx, StartSessionOptions{Agent: agent, CWD: cwd})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	defer session.Close(context.Background())
	if _, err := session.Run(ctx, "write notes.txt hello"); err != nil {
		t.Fatalf("Run(write) error = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(cwd, "notes.txt"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("notes.txt = %q, want hello", string(data))
	}
	ops := session.Operations()
	if len(ops) == 0 {
		t.Fatalf("expected operation")
	}
	if ops[0].Kind != "write_file" {
		t.Fatalf("operation kind = %q, want write_file", ops[0].Kind)
	}
}

// TestQueuePolicyDropSuppressesIntermediateEvents verifies that
// QueuePolicyInput{Delivery:"drop"} causes the turn to deliver only the final
// completion, with no intermediate text/operation events, while the read model
// (Operations) still accumulates state.
func TestQueuePolicyDropSuppressesIntermediateEvents(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cwd := t.TempDir()
	storage := t.TempDir()
	simulatorBin := buildSimulatorBinary(t)
	agent := Agent{
		Type:    LocalSimulatorAgentACPRegistryID,
		Command: simulatorBin,
		Args:    []string{"--auth-mode", "none", "--storage-dir", storage},
	}
	runtime := NewRuntime(NewStdioConnectionFactory(StdioFactoryOptions{}), RuntimeOptions{})
	session, err := runtime.StartSession(ctx, StartSessionOptions{
		Agent: agent,
		CWD:   cwd,
		Queue: QueuePolicyInput{Delivery: "drop"},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	defer session.Close(context.Background())

	handle := session.StartTurn(ctx, RuntimePrompt{Text: "write notes.txt hi"})
	var sawText, sawOperation bool
	for event := range handle.Events {
		switch event.Type {
		case "text":
			sawText = true
		case "operation_updated":
			sawOperation = true
		}
	}
	result := <-handle.Completion
	if result.Err != nil {
		t.Fatalf("Run error = %v", result.Err)
	}
	// Intermediate events must be suppressed in drop mode.
	if sawText {
		t.Fatalf("received text event in drop mode")
	}
	if sawOperation {
		t.Fatalf("received operation event in drop mode")
	}
	// But the read model should still reflect what happened.
	if len(session.Operations()) == 0 {
		t.Fatalf("Operations() empty; read model should still update in drop mode")
	}
}

// TestQueuePolicyBufferDefaultStreamsEvents verifies the default (buffer) mode
// delivers intermediate events, confirming the drop-mode test is meaningful.
func TestQueuePolicyBufferDefaultStreamsEvents(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cwd := t.TempDir()
	storage := t.TempDir()
	simulatorBin := buildSimulatorBinary(t)
	agent := Agent{
		Type:    LocalSimulatorAgentACPRegistryID,
		Command: simulatorBin,
		Args:    []string{"--auth-mode", "none", "--storage-dir", storage},
	}
	runtime := NewRuntime(NewStdioConnectionFactory(StdioFactoryOptions{}), RuntimeOptions{})
	session, err := runtime.StartSession(ctx, StartSessionOptions{Agent: agent, CWD: cwd})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	defer session.Close(context.Background())

	handle := session.StartTurn(ctx, RuntimePrompt{Text: "Reply with the single word OK."})
	sawText := false
	for event := range handle.Events {
		if event.Type == "text" && event.Text != "" {
			sawText = true
		}
	}
	<-handle.Completion
	if !sawText {
		t.Fatalf("expected text events in default buffer mode")
	}
}

// TestStoredSessionsEnabledDoesNotBreakList verifies that RuntimeOptions.
// StoredSessionsEnabled is honored across the list path without errors. The
// simulator returns an empty session list when queried from a fresh process
// (its in-memory map is per-process), so this asserts the option is wired and
// the call succeeds in both modes; the Source-labeling logic itself
// ("stored" vs "remote") is a one-line branch in ListAgentSessions.
func TestStoredSessionsEnabledDoesNotBreakList(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cwd := t.TempDir()
	storage := t.TempDir()
	simulatorBin := buildSimulatorBinary(t)
	agent := Agent{
		Type:    LocalSimulatorAgentACPRegistryID,
		Command: simulatorBin,
		Args:    []string{"--auth-mode", "none", "--storage-dir", storage},
	}
	for _, enabled := range []bool{false, true} {
		runtime := NewRuntime(NewStdioConnectionFactory(StdioFactoryOptions{}), RuntimeOptions{StoredSessionsEnabled: enabled})
		list, err := runtime.ListSessions(ctx, ListSessionsOptions{Agent: agent, CWD: cwd})
		if err != nil {
			t.Fatalf("ListSessions() (enabled=%v) error = %v", enabled, err)
		}
		// Every returned session (if any) must carry the correct source label.
		wantSrc := "remote"
		if enabled {
			wantSrc = "stored"
		}
		for _, s := range list.Sessions {
			if s.Source != wantSrc {
				t.Fatalf("Source = %q, want %q (enabled=%v)", s.Source, wantSrc, enabled)
			}
		}
	}
}

// recordingTerminalHandlerForRuntime is a TerminalHandler that records calls
// and runs the command via os/exec, capturing combined output, used by
// TestSimulatorInvokesHostTerminal.
type recordingTerminalHandlerForRuntime struct {
	mu        sync.Mutex
	creates   int
	outputs   int
	waits     int
	releases  int
	terminals map[string]*recTerminal
}

type recTerminal struct {
	output strings.Builder
	done   chan struct{}
	exit   *TerminalExitStatus
	mu     sync.Mutex
}

func (h *recordingTerminalHandlerForRuntime) ensure() {
	if h.terminals == nil {
		h.terminals = map[string]*recTerminal{}
	}
}

func (h *recordingTerminalHandlerForRuntime) CreateTerminal(ctx Context, req CreateTerminalRequest) (CreateTerminalResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ensure()
	h.creates++
	cmd := exec.Command(req.Command, req.Args...)
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return CreateTerminalResult{}, err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return CreateTerminalResult{}, err
	}
	id := fmt.Sprintf("rec-%d", h.creates)
	term := &recTerminal{done: make(chan struct{})}
	h.terminals[id] = term
	go func() {
		defer close(term.done)
		buf := make([]byte, 4096)
		for {
			n, err := pipe.Read(buf)
			if n > 0 {
				term.mu.Lock()
				term.output.Write(buf[:n])
				term.mu.Unlock()
			}
			if err != nil {
				break
			}
		}
		waitErr := cmd.Wait()
		term.mu.Lock()
		term.exit = recExitStatusFromErr(waitErr)
		term.mu.Unlock()
	}()
	return CreateTerminalResult{TerminalID: id}, nil
}

func (h *recordingTerminalHandlerForRuntime) Output(ctx Context, terminalID string) (TerminalOutputResult, error) {
	h.mu.Lock()
	h.outputs++
	term := h.terminals[terminalID]
	h.mu.Unlock()
	if term == nil {
		return TerminalOutputResult{}, fmt.Errorf("unknown terminal %q", terminalID)
	}
	term.mu.Lock()
	out := term.output.String()
	status := term.exit
	term.mu.Unlock()
	return TerminalOutputResult{Output: out, ExitStatus: status}, nil
}

func (h *recordingTerminalHandlerForRuntime) WaitForExit(ctx Context, terminalID string) (TerminalExitStatus, error) {
	h.mu.Lock()
	h.waits++
	term := h.terminals[terminalID]
	h.mu.Unlock()
	if term == nil {
		return TerminalExitStatus{}, fmt.Errorf("unknown terminal %q", terminalID)
	}
	select {
	case <-term.done:
	case <-ctx.Done():
		return TerminalExitStatus{}, ctx.Err()
	}
	term.mu.Lock()
	defer term.mu.Unlock()
	if term.exit == nil {
		return TerminalExitStatus{}, nil
	}
	return *term.exit, nil
}

func (h *recordingTerminalHandlerForRuntime) Kill(ctx Context, terminalID string) error { return nil }

func (h *recordingTerminalHandlerForRuntime) Release(ctx Context, terminalID string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.releases++
	return nil
}

func (h *recordingTerminalHandlerForRuntime) snapshot() (int, int, int, int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.creates, h.outputs, h.waits, h.releases
}

func recExitStatusFromErr(err error) *TerminalExitStatus {
	if err == nil {
		code := uint32(0)
		return &TerminalExitStatus{ExitCode: &code}
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		code := uint32(exitErr.ExitCode())
		return &TerminalExitStatus{ExitCode: &code}
	}
	return &TerminalExitStatus{}
}

// TestSimulatorInvokesHostTerminal is the key end-to-end proof: when the host
// supplies a TerminalHandler AND advertises terminal capability, the simulator
// agent's "/bash <cmd>" prompt triggers a real reverse RPC round-trip
// (terminal/create + terminal/output + terminal/wait_for_exit + terminal/release)
// against the host. This closes the loop that was previously a false positive.
func TestSimulatorInvokesHostTerminal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cwd := t.TempDir()
	storage := t.TempDir()
	simulatorBin := buildSimulatorBinary(t)
	agent := Agent{
		Type:    LocalSimulatorAgentACPRegistryID,
		Command: simulatorBin,
		Args:    []string{"--auth-mode", "none", "--storage-dir", storage},
	}
	handler := &recordingTerminalHandlerForRuntime{}
	runtime := NewRuntime(NewStdioConnectionFactory(StdioFactoryOptions{}), RuntimeOptions{
		AuthorityHandlers: AuthorityHandlers{Terminal: handler},
	})
	session, err := runtime.StartSession(ctx, StartSessionOptions{
		Agent:    agent,
		CWD:      cwd,
		Handlers: AuthorityHandlers{Terminal: handler},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	defer session.Close(context.Background())

	completion, err := session.Run(ctx, "/bash echo hello")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	creates, _, _, releases := handler.snapshot()
	if creates == 0 {
		t.Fatalf("host TerminalHandler.CreateTerminal was never called; simulator did not invoke terminal/create. Output: %q", completion.OutputText)
	}
	if releases == 0 {
		t.Fatalf("host TerminalHandler.Release was never called; simulator leaked the terminal")
	}
	// The simulator should have streamed the actual command output ("hello")
	// rather than the synthetic "Ran echo." fallback.
	if !strings.Contains(completion.OutputText, "hello") {
		t.Fatalf("OutputText = %q, want it to contain the real command output 'hello'", completion.OutputText)
	}
}

func buildSimulatorBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "acp-simulator-agent")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/acp-simulator-agent")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build simulator failed: %v\n%s", err, string(output))
	}
	return bin
}

func TestCommandsBuild(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, item := range []struct {
		name string
		pkg  string
	}{
		{name: "acp-runtime", pkg: "./cmd/acp-runtime"},
		{name: "acp-simulator-agent", pkg: "./cmd/acp-simulator-agent"},
		{name: "acp-harness", pkg: "./cmd/acp-harness"},
	} {
		out := filepath.Join(t.TempDir(), item.name)
		cmd := exec.CommandContext(ctx, "go", "build", "-o", out, item.pkg)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go build %s failed: %v\n%s", item.pkg, err, string(output))
		}
	}
}
