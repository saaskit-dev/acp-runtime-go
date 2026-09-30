package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	acp "github.com/saaskit-dev/acp-runtime-go"
)

type Runner struct {
	Runtime *acp.Runtime
	Factory acp.ConnectionFactory
	Agent   acp.Agent
	CWD     string
}

func LoadCase(path string) (Case, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Case{}, err
	}
	var c Case
	err = json.Unmarshal(data, &c)
	return c, err
}
func (r Runner) Run(ctx context.Context, c Case) (result Result, err error) {
	result = Result{CaseID: c.ID, Status: "FAIL"}
	if c.Experimental {
		result.Status = "SKIPPED"
		result.Reason = "experimental case excluded from stable contract"
		return result, nil
	}
	if len(c.Agents.Include) > 0 && !contains(c.Agents.Include, r.Agent.Type) || contains(c.Agents.Exclude, r.Agent.Type) {
		result.Status = "SKIPPED"
		result.Reason = "case does not apply to agent " + r.Agent.Type
		return result, nil
	}
	if len(c.Steps) == 0 {
		return result, fmt.Errorf("case requires at least one step")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cwd := r.CWD
	if cwd == "" {
		cwd, err = os.Getwd()
		if err != nil {
			return result, err
		}
	}
	cwd, err = filepath.Abs(cwd)
	if err != nil {
		return result, err
	}
	fixture, err := os.MkdirTemp("", "acp-harness-")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(fixture)
	for name, content := range c.Fixtures {
		path := filepath.Join(fixture, name)
		rel, relErr := filepath.Rel(fixture, path)
		if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return result, fmt.Errorf("invalid fixture path %q", name)
		}
		if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return result, err
		}
		if err = os.WriteFile(path, []byte(content), 0600); err != nil {
			return result, err
		}
	}
	expand := func(s string) string { return strings.NewReplacer("$cwd", cwd, "$fixture", fixture).Replace(s) }
	rec := newRecorder()
	observe := func(_ context.Context, _ acp.ConnectionFactoryInput, h *acp.ConnectionHandle) {
		id := rec.connection()
		h.Connection.SetRawMessageObserver(func(direction string, raw json.RawMessage) { rec.observe(id, direction, raw) })
	}
	factory := r.Factory
	if factory == nil {
		factory = acp.NewStdioConnectionFactory(acp.StdioFactoryOptions{})
	}
	runtime := r.Runtime
	if runtime == nil {
		runtime = acp.NewRuntime(factory, acp.RuntimeOptions{})
	}
	runtime.SetConnectionObserver(observe)
	defer func() {
		result.Transcript = rec.snapshot()
		if err == nil {
			err = validateAssertions(c, result)
		}
		if err != nil {
			result.Status = "FAIL"
			result.Reason = err.Error()
			var unsupported *UnsupportedError
			if errors.As(err, &unsupported) {
				result.Status = "UNSUPPORTED"
			}
		}
	}()
	terminal := &harnessTerminalHandler{}
	defer terminal.close()
	var permissionMu sync.Mutex
	decision := "deny"
	handlers := acp.AuthorityHandlers{Terminal: terminal, Filesystem: harnessFilesystem{}, Permission: func(_ acp.Context, req acp.PermissionRequest) (acp.PermissionDecision, error) {
		permissionMu.Lock()
		d := decision
		permissionMu.Unlock()
		prefix := "reject"
		if d == "allow" {
			prefix = "allow"
		}
		for _, option := range req.Options {
			if option.Kind == prefix+"_once" || option.Kind == prefix+"_always" {
				return acp.PermissionDecision{Outcome: "selected", OptionID: option.ID}, nil
			}
		}
		return acp.PermissionDecision{Outcome: "cancelled"}, nil
	}}
	start := acp.StartSessionOptions{Agent: r.Agent, CWD: cwd, Handlers: handlers}
	var session *acp.Session
	var currentID string
	var currentConnection uint64
	var sessions []*acp.Session
	defer func() {
		closeCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		for i := len(sessions) - 1; i >= 0; i-- {
			if closeErr := sessions[i].Close(closeCtx); closeErr != nil && err == nil {
				err = fmt.Errorf("close session: %w", closeErr)
			}
		}
	}()
	var probeHandle acp.ConnectionHandle
	var initialized acp.InitializeResponse
	defer func() {
		if probeHandle.Dispose != nil {
			closeCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			if closeErr := probeHandle.Dispose(closeCtx); closeErr != nil && err == nil {
				err = closeErr
			}
		}
	}()
	var turn *acp.TurnHandle
	var turnRef string
	var turnStart uint64
	awaitTurn := func() error {
		if turn == nil {
			return nil
		}
		select {
		case done, ok := <-turn.Completion:
			turn = nil
			if !ok {
				return fmt.Errorf("completion channel closed without result")
			}
			return done.Err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	setSession := func(s *acp.Session) {
		session = s
		currentID = s.Snapshot().Session.ID
		sessions = append(sessions, s)
		for _, entry := range rec.snapshot() {
			if entry.SessionID == currentID && len(entry.Response) > 0 && (entry.Method == "session/new" || entry.Method == "session/load" || entry.Method == "session/resume" || entry.Method == "session/fork") {
				currentConnection = entry.ConnectionID
			}
		}
	}
	for _, step := range c.Steps {
		switch step.Type {
		case "initialize":
			if r.Factory == nil && (r.Agent.Type == acp.CodexNativeRegistryID || r.Agent.Type == acp.ClaudeCodeNativeRegistryID) {
				err = &UnsupportedError{Reason: "standalone native initialize requires an explicit loopback Factory"}
				break
			}
			if probeHandle.Connection != nil {
				return result, fmt.Errorf("duplicate initialize step")
			}
			if r.Agent.Command == "" {
				return result, fmt.Errorf("initialize requires an explicit agent command")
			}
			input := acp.ConnectionFactoryInput{Agent: r.Agent, CWD: cwd, Client: acp.Client{Info: acp.Implementation{Name: "acp-harness", Version: "1"}}}
			probeHandle, err = factory(ctx, input)
			if err == nil {
				observe(ctx, input, &probeHandle)
				initialized, err = probeHandle.Connection.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: acp.ProtocolVersion, ClientInfo: &input.Client.Info})
			}
		case "authenticate":
			if probeHandle.Connection == nil {
				err = fmt.Errorf("authenticate requires initialize")
				break
			}
			if len(initialized.AuthMethods) == 0 {
				err = &UnsupportedError{Reason: "agent advertised no authentication methods"}
				break
			}
			_, err = probeHandle.Connection.Authenticate(ctx, acp.AuthenticateRequest{MethodID: initialized.AuthMethods[0].ID})
		case "session-new":
			var s *acp.Session
			s, err = runtime.StartSession(ctx, start)
			if err == nil {
				setSession(s)
			}
		case "session-list":
			_, err = runtime.ListSessions(ctx, acp.ListSessionsOptions{Agent: r.Agent, CWD: cwd})
		case "session-load", "session-resume", "session-fork":
			if currentID == "" {
				err = fmt.Errorf("%s requires session", step.Type)
				break
			}
			if err = awaitTurn(); err != nil {
				break
			}
			var s *acp.Session
			switch step.Type {
			case "session-load":
				s, err = runtime.LoadSession(ctx, acp.LoadSessionOptions{StartSessionOptions: start, SessionID: currentID})
			case "session-resume":
				s, err = runtime.ResumeSession(ctx, acp.ResumeSessionOptions{StartSessionOptions: start, SessionID: currentID})
			case "session-fork":
				err = &UnsupportedError{Reason: "session/fork is experimental and excluded from stable contract"}
			}
			if err == nil {
				setSession(s)
			}
		case "set-mode":
			if session == nil {
				err = fmt.Errorf("set-mode requires session")
				break
			}
			mode := step.ModeID
			if mode == "$probe-mode" {
				mode = probeFor(c, r.Agent.Type).ModeID
				if mode == "" && step.SkipIf == "!probe.modeId" {
					continue
				}
			}
			if len(session.Metadata().AgentModes) == 0 {
				err = &UnsupportedError{Reason: "agent advertised no modes"}
				break
			}
			err = session.SetAgentMode(ctx, resolveModeID(mode, session.Metadata().AgentModes))
		case "set-config-option":
			if session == nil {
				err = fmt.Errorf("set-config-option requires session")
				break
			}
			options := session.Metadata().AgentConfigOptions
			if len(options) == 0 {
				err = &UnsupportedError{Reason: "agent advertised no config options"}
				break
			}
			id, value := resolveConfigOption(step, options)
			err = session.SetAgentConfigOption(ctx, id, value)
		case "permission-decision":
			if session == nil {
				err = fmt.Errorf("permission-decision requires session")
				break
			}
			if step.Decision != "allow" && step.Decision != "deny" {
				err = fmt.Errorf("unknown permission decision %q", step.Decision)
				break
			}
			permissionMu.Lock()
			decision = step.Decision
			permissionMu.Unlock()
		case "session-prompt":
			if session == nil {
				err = fmt.Errorf("session-prompt requires session")
				break
			}
			if err = awaitTurn(); err != nil {
				break
			}
			prompt := step.Prompt
			if prompt == "$probe-prompt" {
				prompt = probeFor(c, r.Agent.Type).Prompt
			}
			if strings.HasPrefix(prompt, "$") && prompt != "$fixture" {
				prompt = ""
			}
			prompt = firstNonEmpty(prompt, step.DefaultPrompt, "Reply with the single word OK.")
			turnStart = rec.position()
			t := session.StartTurn(ctx, acp.RuntimePrompt{Text: expand(prompt)})
			turn = &t
			turnRef = step.TurnRef
			go func(events <-chan acp.TurnEvent) {
				for range events {
				}
			}(t.Events)
		case "wait-for-event":
			event := firstNonEmpty(step.EventType, "completed")
			timeout := time.Duration(step.TimeoutMS) * time.Millisecond
			if timeout <= 0 {
				timeout = 5 * time.Second
			}
			waitCtx, stop := context.WithTimeout(ctx, timeout)
			for {
				found := false
				for _, entry := range rec.snapshot() {
					if witnessed(entry) && entry.Sequence > turnStart && entry.EventType == event && entry.ConnectionID == currentConnection && entry.SessionID == currentID {
						found = true
						break
					}
				}
				if found {
					break
				}
				select {
				case <-rec.changed:
				case <-waitCtx.Done():
					err = fmt.Errorf("waiting for real %s: %w", event, waitCtx.Err())
				}
				if err != nil {
					break
				}
			}
			stop()
			if err == nil && event == "completed" {
				err = awaitTurn()
			}
		case "session-cancel":
			if session == nil || turn == nil {
				err = fmt.Errorf("session-cancel requires active turn")
				break
			}
			if step.TurnRef != "" && step.TurnRef != turnRef {
				err = fmt.Errorf("unknown turnRef %q", step.TurnRef)
				break
			}
			for _, entry := range rec.snapshot() {
				if entry.Sequence > turnStart && entry.Terminal && entry.ConnectionID == currentConnection && entry.SessionID == currentID {
					err = fmt.Errorf("cancel after completed turn")
				}
			}
			if err == nil {
				_, err = session.CancelTurn(ctx, turn.TurnID)
			}
		case "session-delete":
			if session == nil {
				err = fmt.Errorf("session-delete requires session")
			} else {
				err = session.Delete(ctx)
			}
		case "logout":
			if session == nil {
				err = fmt.Errorf("logout requires session")
			} else {
				err = session.Logout(ctx)
			}
		default:
			err = &UnsupportedError{Reason: fmt.Sprintf("case step %q", step.Type)}
		}
		if err != nil {
			return result, err
		}
	}
	if err = awaitTurn(); err != nil {
		return result, err
	}
	result.Transcript = rec.snapshot()
	if err = validateAssertions(c, result); err != nil {
		return result, err
	}
	result.Status = "PASS"
	return result, nil
}
func contains(items []string, want string) bool {
	for _, item := range items {
		if canonicalAgent(item) == canonicalAgent(want) {
			return true
		}
	}
	return false
}
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
func resolveModeID(input string, modes []acp.RuntimeAgentMode) string {
	if input != "" && input != "$alternate" && input != "$probe-mode" {
		return input
	}
	for _, mode := range modes {
		if mode.ID == "yolo" {
			return mode.ID
		}
	}
	if len(modes) > 0 {
		return modes[0].ID
	}
	return ""
}
func resolveConfigOption(step CaseStep, options []acp.RuntimeAgentConfigOption) (string, any) {
	option := options[0]
	if step.Key != "" && step.Key != "$first" {
		for _, candidate := range options {
			if candidate.ID == step.Key {
				option = candidate
				break
			}
		}
	}
	value := step.Value
	if value == nil || value == "$first-value" {
		value = option.Value
		if len(option.Options) > 0 {
			value = option.Options[0].Value
		}
	}
	return option.ID, value
}
func RunCaseFile(ctx context.Context, path string, agent acp.Agent, cwd string) (Result, error) {
	c, err := LoadCase(path)
	if err != nil {
		return Result{}, err
	}
	return (Runner{Agent: agent, CWD: cwd}).Run(ctx, c)
}
func DefaultCasePath(name string) string { return filepath.Join("harness", "cases", name) }

type harnessFilesystem struct{}

func (harnessFilesystem) ReadTextFile(_ acp.Context, path string) (string, error) {
	data, err := os.ReadFile(path)
	return string(data), err
}
func (harnessFilesystem) WriteTextFile(_ acp.Context, path, text string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(text), 0600)
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

func (h *harnessTerminalHandler) close() {
	h.mu.Lock()
	ids := make([]string, 0, len(h.terminals))
	for id := range h.terminals {
		ids = append(ids, id)
	}
	h.mu.Unlock()
	for _, id := range ids {
		_ = h.Release(context.Background(), id)
	}
}

func canonicalAgent(id string) string {
	if id == "simulator-agent-acp-local" || id == acp.LocalSimulatorAgentACPRegistryID {
		return acp.LocalSimulatorAgentACPRegistryID
	}
	return id
}
func probeFor(c Case, id string) Probe {
	for key, probe := range c.Probes {
		if canonicalAgent(key) == canonicalAgent(id) {
			return probe
		}
	}
	return Probe{}
}
