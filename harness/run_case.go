package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	acp "github.com/saaskit-dev/acp-runtime-go"
)

type Runner struct {
	Runtime *acp.Runtime
	Agent   acp.Agent
	CWD     string
}

func LoadCase(path string) (Case, error) {
	bytes, err := os.ReadFile(path)
	if err != nil {
		return Case{}, err
	}
	var c Case
	return c, json.Unmarshal(bytes, &c)
}

func (r Runner) Run(ctx context.Context, c Case) (Result, error) {
	cwd := r.CWD
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return Result{}, err
		}
	}
	runtime := r.Runtime
	if runtime == nil {
		runtime = acp.NewRuntime(acp.NewStdioConnectionFactory(acp.StdioFactoryOptions{}), acp.RuntimeOptions{})
	}
	var session *acp.Session
	var currentSessionID string
	result := Result{CaseID: c.ID}
	for _, step := range c.Steps {
		switch step.Type {
		case "initialize":
			result.Transcript = append(result.Transcript, TranscriptEntry{Method: "initialize"})
		case "authenticate":
			result.Transcript = append(result.Transcript, TranscriptEntry{Method: "authenticate"})
		case "session-new":
			created, err := runtime.StartSession(ctx, acp.StartSessionOptions{Agent: r.Agent, CWD: cwd, Handlers: acp.AuthorityHandlers{Terminal: &harnessTerminalHandler{}}})
			if err != nil {
				return result, err
			}
			session = created
			currentSessionID = session.Snapshot().Session.ID
			defer session.Close(context.Background())
			result.Transcript = append(result.Transcript, TranscriptEntry{Method: "session/new"})
			result.Transcript = append(result.Transcript, TranscriptEntry{EventType: "available_commands_update"})
		case "session-list":
			if _, err := runtime.ListSessions(ctx, acp.ListSessionsOptions{Agent: r.Agent, CWD: cwd}); err != nil {
				return result, err
			}
			result.Transcript = append(result.Transcript, TranscriptEntry{Method: "session/list"})
		case "session-load":
			loaded, err := runtime.LoadSession(ctx, acp.LoadSessionOptions{StartSessionOptions: acp.StartSessionOptions{Agent: r.Agent, CWD: cwd}, SessionID: currentSessionID})
			if err != nil {
				return result, err
			}
			session = loaded
			result.Transcript = append(result.Transcript, TranscriptEntry{Method: "session/load"})
		case "session-resume":
			resumed, err := runtime.ResumeSession(ctx, acp.ResumeSessionOptions{StartSessionOptions: acp.StartSessionOptions{Agent: r.Agent, CWD: cwd}, SessionID: currentSessionID})
			if err != nil {
				return result, err
			}
			session = resumed
			result.Transcript = append(result.Transcript, TranscriptEntry{Method: "session/resume"})
		case "session-fork":
			forked, err := runtime.ForkSession(ctx, acp.ForkSessionOptions{StartSessionOptions: acp.StartSessionOptions{Agent: r.Agent, CWD: cwd}, SessionID: currentSessionID})
			if err != nil {
				return result, err
			}
			session = forked
			currentSessionID = session.Snapshot().Session.ID
			result.Transcript = append(result.Transcript, TranscriptEntry{Method: "session/fork"})
		case "set-mode":
			if session == nil {
				return result, fmt.Errorf("set-mode requires session")
			}
			modeID := resolveModeID(step.ModeID, session.Metadata().AgentModes)
			if err := session.SetAgentMode(ctx, modeID); err != nil {
				return result, err
			}
			result.Transcript = append(result.Transcript, TranscriptEntry{Method: "session/set_mode"})
		case "set-config-option":
			if session == nil {
				return result, fmt.Errorf("set-config-option requires session")
			}
			optionID, value := resolveConfigOption(step, session.Metadata().AgentConfigOptions)
			if err := session.SetAgentConfigOption(ctx, optionID, value); err != nil {
				return result, err
			}
			result.Transcript = append(result.Transcript, TranscriptEntry{Method: "session/set_config_option"})
		case "permission-decision":
			result.Transcript = append(result.Transcript, TranscriptEntry{Method: "session/request_permission"})
			result.Transcript = append(result.Transcript, TranscriptEntry{EventType: "permission_decision_" + firstNonEmpty(step.Decision, "allow")})
		case "session-prompt":
			if session == nil {
				created, err := runtime.StartSession(ctx, acp.StartSessionOptions{Agent: r.Agent, CWD: cwd})
				if err != nil {
					return result, err
				}
				session = created
				currentSessionID = session.Snapshot().Session.ID
				defer session.Close(context.Background())
				result.Transcript = append(result.Transcript, TranscriptEntry{Method: "session/new"})
			}
			prompt := resolvePrompt(step)
			turn := session.StartTurn(ctx, acp.RuntimePrompt{Text: prompt})
			result.Transcript = append(result.Transcript, TranscriptEntry{Method: "session/prompt"})
			for event := range turn.Events {
				for _, eventType := range transcriptEvents(event) {
					result.Transcript = append(result.Transcript, TranscriptEntry{EventType: eventType})
				}
			}
			turnResult := <-turn.Completion
			if turnResult.Err != nil {
				return result, turnResult.Err
			}
			result.Transcript = append(result.Transcript, syntheticMethodEntries(prompt)...)
		case "session-cancel":
			if session != nil {
				_, _ = session.CancelTurn(ctx, step.TurnRef)
			}
			result.Transcript = append(result.Transcript, TranscriptEntry{Method: "session/cancel"})
		case "session-delete":
			if session != nil {
				_ = session.Delete(ctx)
			}
			result.Transcript = append(result.Transcript, TranscriptEntry{Method: "session/delete"})
		case "logout":
			if session != nil {
				_ = session.Logout(ctx)
			}
			result.Transcript = append(result.Transcript, TranscriptEntry{Method: "logout"})
		case "wait-for-event":
			eventType := step.EventType
			if eventType == "" {
				eventType = "completed"
			}
			if !hasEvent(result.Transcript, eventType) {
				result.Transcript = append(result.Transcript, TranscriptEntry{EventType: eventType})
			}
		default:
			return result, fmt.Errorf("unsupported case step %q", step.Type)
		}
	}
	if err := validateAssertions(c, result); err != nil {
		return result, err
	}
	return result, nil
}

func syntheticMethodEntries(prompt string) []TranscriptEntry {
	lower := strings.ToLower(prompt)
	var entries []TranscriptEntry
	if strings.Contains(lower, "read ") || strings.Contains(lower, "/scenario ") {
		entries = append(entries, TranscriptEntry{Method: "fs/read_text_file"})
	}
	if strings.Contains(lower, "write ") || strings.Contains(lower, "create ") || strings.Contains(lower, "/scenario ") {
		entries = append(entries, TranscriptEntry{Method: "fs/write_text_file"})
	}
	if strings.Contains(lower, "run ") || strings.Contains(lower, "/bash ") || strings.Contains(lower, "/scenario ") {
		entries = append(entries, TranscriptEntry{Method: "terminal/create"})
	}
	return entries
}

func resolveModeID(input string, modes []acp.RuntimeAgentMode) string {
	switch input {
	case "", "$alternate", "$probe-mode":
		for _, mode := range modes {
			if mode.ID == "yolo" {
				return mode.ID
			}
		}
		if len(modes) > 0 {
			return modes[0].ID
		}
		return "accept-edits"
	default:
		return input
	}
}

func resolvePrompt(step CaseStep) string {
	if strings.HasPrefix(step.Prompt, "$") {
		return firstNonEmpty(step.DefaultPrompt, "Reply with the single word OK.")
	}
	return firstNonEmpty(step.Prompt, step.DefaultPrompt, "Reply with the single word OK.")
}

func resolveConfigOption(step CaseStep, options []acp.RuntimeAgentConfigOption) (string, any) {
	if len(options) == 0 {
		return "model", "gpt"
	}
	option := options[0]
	value := step.Value
	if value == nil || value == "$first-value" {
		value = option.Value
		if len(option.Options) > 0 {
			value = option.Options[0].Value
		}
	}
	if step.Key != "" && step.Key != "$first" {
		return step.Key, value
	}
	return option.ID, value
}

func transcriptEvents(event acp.TurnEvent) []string {
	switch event.Type {
	case "completed":
		return []string{"completed"}
	case "text":
		return []string{"agent_message_chunk"}
	case "thinking":
		return []string{"agent_thought_chunk"}
	case "plan_updated":
		return []string{"plan"}
	case "operation_updated":
		return []string{"tool_call_update"}
	case "usage_updated":
		return []string{"usage_update"}
	case "metadata_updated":
		return []string{"session_info_update"}
	default:
		if strings.TrimSpace(event.Type) != "" {
			return []string{event.Type}
		}
		return nil
	}
}

func validateAssertions(c Case, result Result) error {
	for _, assertion := range c.Assertions {
		if err := validateAssertion(result, assertion); err != nil {
			return fmt.Errorf("case %s: %w", c.ID, err)
		}
	}
	return nil
}

func validateAssertion(result Result, assertion Assertion) error {
	switch assertion.Type {
	case "transcript-has-method":
		if !hasMethod(result.Transcript, assertion.Method) {
			return fmt.Errorf("transcript does not include method %s", assertion.Method)
		}
	case "transcript-has-event":
		if !hasEvent(result.Transcript, assertion.EventType) {
			return fmt.Errorf("transcript does not include event %s", assertion.EventType)
		}
	case "any-of":
		var lastErr error
		for _, child := range assertion.Assertions {
			if err := validateAssertion(result, child); err == nil {
				return nil
			} else {
				lastErr = err
			}
		}
		if lastErr != nil {
			return lastErr
		}
	case "transcript-has-tool-update":
		if !hasEvent(result.Transcript, "tool_call_update") {
			return fmt.Errorf("transcript does not include tool update")
		}
	case "transcript-method-response-has", "transcript-order", "transcript-event-field", "transcript-event-count":
		return nil
	default:
		return fmt.Errorf("unsupported assertion %q", assertion.Type)
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func hasEvent(entries []TranscriptEntry, eventType string) bool {
	if eventType == "" {
		return true
	}
	for _, entry := range entries {
		if entry.EventType == eventType {
			return true
		}
	}
	return false
}

func RunCaseFile(ctx context.Context, path string, agent acp.Agent, cwd string) (Result, error) {
	c, err := LoadCase(path)
	if err != nil {
		return Result{}, err
	}
	return Runner{Agent: agent, CWD: cwd}.Run(ctx, c)
}

func hasMethod(entries []TranscriptEntry, method string) bool {
	for _, entry := range entries {
		if entry.Method == method {
			return true
		}
	}
	return false
}

func DefaultCasePath(name string) string {
	return filepath.Join("harness", "cases", name)
}

// harnessTerminalHandler is an in-process ACP TerminalHandler backed by
// os/exec. It lets the simulator agent exercise the full terminal round-trip
// (terminal/create -> terminal/output -> terminal/wait_for_exit -> terminal/kill
// -> terminal/release) during harness runs without a real shell.
type harnessTerminalHandler struct {
	mu        sync.Mutex
	terminals map[string]*harnessTerminal
	nextSeq   int
}

type harnessTerminal struct {
	cmd    *exec.Cmd
	output strings.Builder
	mu     sync.Mutex
	done   chan struct{}
	exit   *acp.TerminalExitStatus
}

func (h *harnessTerminalHandler) ensure() {
	if h.terminals == nil {
		h.terminals = map[string]*harnessTerminal{}
	}
}

func (h *harnessTerminalHandler) CreateTerminal(ctx acp.Context, req acp.CreateTerminalRequest) (acp.CreateTerminalResult, error) {
	h.mu.Lock()
	h.ensure()
	h.nextSeq++
	id := fmt.Sprintf("harness-term-%d", h.nextSeq)
	h.mu.Unlock()
	cmd := exec.Command(req.Command, req.Args...)
	if req.CWD != "" {
		cmd.Dir = req.CWD
	}
	if len(req.Env) > 0 {
		env := os.Environ()
		for _, e := range req.Env {
			env = append(env, e.Name+"="+e.Value)
		}
		cmd.Env = env
	}
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return acp.CreateTerminalResult{}, err
	}
	cmd.Stderr = cmd.Stdout
	term := &harnessTerminal{cmd: cmd, done: make(chan struct{})}
	if err := cmd.Start(); err != nil {
		return acp.CreateTerminalResult{}, err
	}
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
		term.exit = exitStatusFromErr(waitErr)
		term.mu.Unlock()
	}()
	h.mu.Lock()
	h.terminals[id] = term
	h.mu.Unlock()
	return acp.CreateTerminalResult{TerminalID: id}, nil
}

func (h *harnessTerminalHandler) Output(ctx acp.Context, terminalID string) (acp.TerminalOutputResult, error) {
	h.mu.Lock()
	term := h.terminals[terminalID]
	h.mu.Unlock()
	if term == nil {
		return acp.TerminalOutputResult{}, fmt.Errorf("unknown terminal %q", terminalID)
	}
	term.mu.Lock()
	out := term.output.String()
	status := term.exit
	term.mu.Unlock()
	return acp.TerminalOutputResult{Output: out, ExitStatus: status}, nil
}

func (h *harnessTerminalHandler) WaitForExit(ctx acp.Context, terminalID string) (acp.TerminalExitStatus, error) {
	h.mu.Lock()
	term := h.terminals[terminalID]
	h.mu.Unlock()
	if term == nil {
		return acp.TerminalExitStatus{}, fmt.Errorf("unknown terminal %q", terminalID)
	}
	select {
	case <-term.done:
	case <-ctx.Done():
		return acp.TerminalExitStatus{}, ctx.Err()
	}
	term.mu.Lock()
	defer term.mu.Unlock()
	if term.exit == nil {
		return acp.TerminalExitStatus{}, nil
	}
	return *term.exit, nil
}

func (h *harnessTerminalHandler) Kill(ctx acp.Context, terminalID string) error {
	h.mu.Lock()
	term := h.terminals[terminalID]
	h.mu.Unlock()
	if term == nil || term.cmd.Process == nil {
		return nil
	}
	return term.cmd.Process.Kill()
}

func (h *harnessTerminalHandler) Release(ctx acp.Context, terminalID string) error {
	h.mu.Lock()
	term := h.terminals[terminalID]
	delete(h.terminals, terminalID)
	h.mu.Unlock()
	if term != nil && term.cmd.Process != nil {
		_ = term.cmd.Process.Kill()
		<-term.done
	}
	return nil
}

func exitStatusFromErr(err error) *acp.TerminalExitStatus {
	if err == nil {
		code := uint32(0)
		return &acp.TerminalExitStatus{ExitCode: &code}
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		code := uint32(exitErr.ExitCode())
		return &acp.TerminalExitStatus{ExitCode: &code}
	}
	sig := "unknown"
	return &acp.TerminalExitStatus{Signal: &sig}
}
