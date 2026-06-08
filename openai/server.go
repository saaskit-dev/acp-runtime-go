package openai

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	acp "github.com/saaskit-dev/acp-runtime-go"
)

const (
	headerSessionID      = "X-ACP-Session-ID"
	headerSessionMode    = "X-ACP-Session-Mode"
	headerInputMode      = "X-ACP-Input-Mode"
	headerCWD            = "X-ACP-CWD"
	headerSessionTTL     = "X-ACP-Session-TTL-Seconds"
	defaultSessionTTL    = 30 * time.Minute
	defaultCleanupPeriod = time.Minute
)

type Config struct {
	Runtime           *acp.Runtime
	ConnectionFactory acp.ConnectionFactory
	RuntimeOptions    acp.RuntimeOptions
	DefaultAgentID    string
	ResolveAgent      func(context.Context, string) (acp.Agent, error)
	CWD               string
	SessionTTL        time.Duration
	APIKey            string
	AllowHeaderCWD    bool
	Models            []string
}

type Server struct {
	runtime        *acp.Runtime
	ownsRuntime    bool
	ctx            context.Context
	cancel         context.CancelFunc
	defaultAgentID string
	cwd            string
	sessionTTL     time.Duration
	apiKey         string
	allowHeaderCWD bool
	models         []string
	resolveAgent   func(context.Context, string) (acp.Agent, error)

	mu        sync.Mutex
	sessions  map[string]*sessionRecord
	done      chan struct{}
	closeOnce sync.Once
}

type sessionRecord struct {
	id          string
	acpID       string
	session     *acp.Session
	managed     bool
	agentID     string
	cwd         string
	ownerHash   string
	fingerprint string
	systemHash  string
	createdAt   time.Time
	lastSeenAt  time.Time
	expiresAt   time.Time

	mu      sync.Mutex
	busy    bool
	tainted bool
}

type requestContext struct {
	ownerHash   string
	agentID     string
	cwd         string
	fingerprint string
	systemHash  string
}

type requestValidationError struct {
	param   string
	message string
}

func (e requestValidationError) Error() string {
	return e.message
}

func NewServer(config Config) *Server {
	runtime := config.Runtime
	ownsRuntime := runtime == nil
	if runtime == nil {
		factory := config.ConnectionFactory
		if factory == nil {
			factory = acp.NewStdioConnectionFactory(acp.StdioFactoryOptions{})
		}
		runtime = acp.NewRuntime(factory, config.RuntimeOptions)
	}
	ttl := config.SessionTTL
	if ttl <= 0 {
		ttl = defaultSessionTTL
	}
	cwd := config.CWD
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	defaultAgentID := config.DefaultAgentID
	if defaultAgentID == "" {
		defaultAgentID = acp.LocalSimulatorAgentACPRegistryID
	}
	models := append([]string(nil), config.Models...)
	if len(models) == 0 {
		models = []string{defaultAgentID, "claude", "codex", "gemini", "opencode", "pi", "simulator"}
	}
	models = uniqueStrings(models)
	serverCtx, cancel := context.WithCancel(context.Background())
	server := &Server{
		runtime:        runtime,
		ownsRuntime:    ownsRuntime,
		ctx:            serverCtx,
		cancel:         cancel,
		defaultAgentID: defaultAgentID,
		cwd:            cwd,
		sessionTTL:     ttl,
		apiKey:         config.APIKey,
		allowHeaderCWD: config.AllowHeaderCWD,
		models:         models,
		resolveAgent:   config.ResolveAgent,
		sessions:       map[string]*sessionRecord{},
		done:           make(chan struct{}),
	}
	if server.resolveAgent == nil {
		server.resolveAgent = acp.ResolveRuntimeAgentFromRegistry
	}
	go server.cleanupLoop()
	return server
}

func (s *Server) Close(ctx context.Context) error {
	var firstErr error
	s.closeOnce.Do(func() {
		close(s.done)
		if s.cancel != nil {
			s.cancel()
		}
		records := s.drainSessions()
		for _, record := range records {
			if err := s.closeManagedSession(ctx, record); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		if s.ownsRuntime && s.runtime != nil {
			if err := s.runtime.Close(ctx); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	})
	return firstErr
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/v1/models", s.withAuth(s.handleModels))
	mux.HandleFunc("/v1/chat/completions", s.withAuth(s.handleChatCompletions))
	mux.HandleFunc("/v1/acp/sessions", s.withAuth(s.handleSessions))
	mux.HandleFunc("/v1/acp/sessions/", s.withAuth(s.handleSessionByID))
	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", "")
		return
	}
	now := time.Now().Unix()
	data := make([]modelObject, 0, len(s.models))
	for _, model := range s.models {
		data = append(data, modelObject{ID: model, Object: "model", Created: now, OwnedBy: "acp"})
	}
	writeJSON(w, http.StatusOK, modelListResponse{Object: "list", Data: data})
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.cleanupExpired()
		ownerHash := ownerHashFromRequest(r)
		s.mu.Lock()
		out := make([]sessionListEntry, 0, len(s.sessions))
		for _, record := range s.sessions {
			if record.ownerHash != ownerHash {
				continue
			}
			record.mu.Lock()
			out = append(out, sessionListEntry{
				ID:           record.id,
				ACPSessionID: record.acpID,
				Object:       "acp.session",
				Agent:        record.agentID,
				CWD:          record.cwd,
				Busy:         record.busy,
				Tainted:      record.tainted,
				CreatedAt:    record.createdAt.Unix(),
				LastSeenAt:   record.lastSeenAt.Unix(),
				ExpiresAt:    record.expiresAt.Unix(),
				Fingerprint:  record.fingerprint,
			})
			record.mu.Unlock()
		}
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, sessionListResponse{Object: "list", Data: out})
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", "")
	}
}

func (s *Server) handleSessionByID(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/v1/acp/sessions/")
	if id == "" || strings.Contains(id, "/") {
		writeError(w, http.StatusNotFound, "not_found", "session not found", "")
		return
	}
	switch r.Method {
	case http.MethodDelete:
		ownerHash := ownerHashFromRequest(r)
		record, ok := s.getSession(id)
		if !ok || record.ownerHash != ownerHash {
			writeError(w, http.StatusNotFound, "not_found", "session not found", "")
			return
		}
		s.removeSession(id)
		_ = s.closeManagedSession(context.Background(), record)
		w.WriteHeader(http.StatusNoContent)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", "")
	}
}

func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", "")
		return
	}
	var req chatCompletionRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8*1024*1024))
	if err := decoder.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid JSON request: "+err.Error(), "")
		return
	}
	if err := validateChatCompletionRequest(req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", err.Error(), err.param)
		return
	}
	if len(req.Messages) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "messages must not be empty", "messages")
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	rc := s.buildRequestContext(r, req)
	mode := strings.ToLower(strings.TrimSpace(r.Header.Get(headerSessionMode)))
	sessionID := strings.TrimSpace(r.Header.Get(headerSessionID))
	persistent := sessionID != "" || mode == "persistent" || metadataString(req.Metadata, "acp_session_mode") == "persistent"

	var record *sessionRecord
	var temporary *acp.Session
	var err error
	if persistent {
		if sessionID == "" {
			record, err = s.createPersistentSession(s.ctx, rc)
		} else {
			record, err = s.validatePersistentSession(sessionID, rc)
		}
		if err != nil {
			s.writeSessionError(w, err)
			return
		}
		w.Header().Set(headerSessionID, record.id)
		w.Header().Set(headerSessionTTL, fmt.Sprintf("%d", int(s.sessionTTL.Seconds())))
	} else {
		temporary, err = s.startSession(ctx, rc)
		if err != nil {
			writeError(w, http.StatusBadGateway, "acp_session_error", err.Error(), "")
			return
		}
		defer temporary.Close(context.Background())
	}

	incremental := sessionID != "" && strings.ToLower(strings.TrimSpace(r.Header.Get(headerInputMode))) != "replay"
	if req.Stream {
		if persistent {
			s.streamPersistent(ctx, w, record, req, incremental)
			return
		}
		s.streamTemporary(ctx, w, temporary, req, rc.agentID)
		return
	}

	var completion acp.TurnCompletion
	if persistent {
		completion, err = s.runPersistent(ctx, record, req, incremental)
	} else {
		prompt := buildPrompt(req, false)
		completion, err = temporary.Run(ctx, prompt)
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, "acp_turn_error", err.Error(), "")
		return
	}
	writeJSON(w, http.StatusOK, completionResponse(req, rc.agentID, completion))
}

func (s *Server) buildRequestContext(r *http.Request, req chatCompletionRequest) requestContext {
	agentID := strings.TrimSpace(req.Model)
	if agentID == "" {
		agentID = s.defaultAgentID
	}
	agentID = strings.TrimPrefix(agentID, "acp/")
	cwd := s.cwd
	if s.allowHeaderCWD {
		if headerCWD := strings.TrimSpace(r.Header.Get(headerCWD)); headerCWD != "" {
			cwd = headerCWD
		}
	}
	if !filepath.IsAbs(cwd) {
		abs, err := filepath.Abs(cwd)
		if err == nil {
			cwd = abs
		}
	}
	system := strings.Join(systemMessages(req.Messages), "\n\n")
	systemHash := ""
	if system != "" {
		systemHash = fingerprint(system)
	}
	fingerprint := fingerprint(agentID, cwd)
	return requestContext{
		ownerHash:   ownerHashFromRequest(r),
		agentID:     agentID,
		cwd:         cwd,
		fingerprint: fingerprint,
		systemHash:  systemHash,
	}
}

func (s *Server) startSession(ctx context.Context, rc requestContext) (*acp.Session, error) {
	agent, err := s.resolveAgent(ctx, rc.agentID)
	if err != nil {
		return nil, err
	}
	return s.runtime.StartSession(ctx, acp.StartSessionOptions{Agent: agent, CWD: rc.cwd})
}

func (s *Server) createPersistentSession(ctx context.Context, rc requestContext) (*sessionRecord, error) {
	session, err := s.startSession(ctx, rc)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	id := newID("acpsess")
	record := &sessionRecord{
		id:          id,
		acpID:       session.Snapshot().Session.ID,
		session:     session,
		managed:     true,
		agentID:     rc.agentID,
		cwd:         rc.cwd,
		ownerHash:   rc.ownerHash,
		fingerprint: rc.fingerprint,
		systemHash:  rc.systemHash,
		createdAt:   now,
		lastSeenAt:  now,
		expiresAt:   now.Add(s.sessionTTL),
	}
	s.mu.Lock()
	s.sessions[id] = record
	s.mu.Unlock()
	return record, nil
}

func (s *Server) validatePersistentSession(id string, rc requestContext) (*sessionRecord, error) {
	record, ok := s.getSession(id)
	if !ok {
		return nil, sessionHTTPError{status: http.StatusNotFound, code: "session_not_found", message: "ACP session not found"}
	}
	if record.ownerHash != rc.ownerHash {
		return nil, sessionHTTPError{status: http.StatusNotFound, code: "session_not_found", message: "ACP session not found"}
	}
	if time.Now().After(record.expiresAt) {
		s.removeSession(id)
		_ = s.closeManagedSession(context.Background(), record)
		return nil, sessionHTTPError{status: http.StatusGone, code: "session_expired", message: "ACP session expired"}
	}
	if record.fingerprint != rc.fingerprint {
		return nil, sessionHTTPError{status: http.StatusConflict, code: "session_conflict", message: "ACP session fingerprint does not match request"}
	}
	if rc.systemHash != "" && record.systemHash != "" && rc.systemHash != record.systemHash {
		return nil, sessionHTTPError{status: http.StatusConflict, code: "session_conflict", message: "ACP session system prompt does not match"}
	}
	record.mu.Lock()
	tainted := record.tainted
	record.mu.Unlock()
	if tainted {
		return nil, sessionHTTPError{status: http.StatusConflict, code: "session_tainted", message: "ACP session is tainted; create a new session"}
	}
	return record, nil
}

func (s *Server) runPersistent(ctx context.Context, record *sessionRecord, req chatCompletionRequest, incremental bool) (acp.TurnCompletion, error) {
	if !record.tryBegin() {
		return acp.TurnCompletion{}, sessionHTTPError{status: http.StatusConflict, code: "session_busy", message: "ACP session is busy"}
	}
	defer record.end(false, s.sessionTTL)
	prompt := buildPrompt(req, incremental)
	return record.session.Run(ctx, prompt)
}

func (s *Server) streamPersistent(ctx context.Context, w http.ResponseWriter, record *sessionRecord, req chatCompletionRequest, incremental bool) {
	if !record.tryBegin() {
		writeError(w, http.StatusConflict, "session_busy", "ACP session is busy", "")
		return
	}
	defer record.end(false, s.sessionTTL)
	prompt := buildPrompt(req, incremental)
	s.streamTurn(ctx, w, record.session, prompt, req, req.Model)
}

func (s *Server) streamTemporary(ctx context.Context, w http.ResponseWriter, session *acp.Session, req chatCompletionRequest, model string) {
	prompt := buildPrompt(req, false)
	s.streamTurn(ctx, w, session, prompt, req, model)
}

func (s *Server) streamTurn(ctx context.Context, w http.ResponseWriter, session *acp.Session, prompt string, req chatCompletionRequest, model string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming_not_supported", "response writer does not support streaming", "")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	streamID := newID("chatcmpl")
	turn := session.StartTurn(ctx, acp.RuntimePrompt{Text: prompt})
	writeSSE(w, flusher, streamChunk(streamID, model, choiceDelta{Role: "assistant"}, nil, nil))
	for event := range turn.Events {
		switch event.Type {
		case "text":
			if event.Text != "" {
				writeSSE(w, flusher, streamChunk(streamID, model, choiceDelta{Content: event.Text}, nil, nil))
			}
		case "operation_updated":
			if event.Operation != nil {
				text := formatOperation(event.Operation)
				if text != "" {
					writeSSE(w, flusher, streamChunk(streamID, model, choiceDelta{Content: text}, nil, nil))
				}
			}
		case "plan_updated":
			text := formatPlan(event.Plan)
			if text != "" {
				writeSSE(w, flusher, streamChunk(streamID, model, choiceDelta{Content: text}, nil, nil))
			}
		case "failed":
			writeSSE(w, flusher, map[string]any{"error": event.Error.Error()})
		}
	}
	result := <-turn.Completion
	if result.Err != nil {
		writeSSE(w, flusher, map[string]any{"error": result.Err.Error()})
		return
	}
	finish := finishReason(result.Completion.StopReason)
	usage := usageFromACP(result.Completion.Usage)
	writeSSE(w, flusher, streamChunk(streamID, model, choiceDelta{}, &finish, usage))
	if req.StreamOptions != nil && req.StreamOptions.IncludeUsage {
		writeSSE(w, flusher, chatCompletionResponse{
			ID:      streamID,
			Object:  "chat.completion.chunk",
			Created: time.Now().Unix(),
			Model:   model,
			Choices: []chatCompletionChoice{},
			Usage:   usage,
		})
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}

func (r *sessionRecord) tryBegin() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.busy || r.tainted {
		return false
	}
	r.busy = true
	return true
}

func (r *sessionRecord) end(tainted bool, ttl time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.busy = false
	if tainted {
		r.tainted = true
	}
	now := time.Now()
	r.lastSeenAt = now
	r.expiresAt = now.Add(ttl)
}

func (s *Server) getSession(id string) (*sessionRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.sessions[id]
	return record, ok
}

func (s *Server) removeSession(id string) {
	s.mu.Lock()
	delete(s.sessions, id)
	s.mu.Unlock()
}

func (s *Server) cleanupLoop() {
	ticker := time.NewTicker(defaultCleanupPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.cleanupExpired()
		case <-s.done:
			return
		}
	}
}

func (s *Server) cleanupExpired() {
	now := time.Now()
	var expired []*sessionRecord
	s.mu.Lock()
	for id, record := range s.sessions {
		if now.After(record.expiresAt) {
			delete(s.sessions, id)
			expired = append(expired, record)
		}
	}
	s.mu.Unlock()
	for _, record := range expired {
		_ = s.closeManagedSession(context.Background(), record)
	}
}

func (s *Server) drainSessions() []*sessionRecord {
	s.mu.Lock()
	records := make([]*sessionRecord, 0, len(s.sessions))
	for _, record := range s.sessions {
		records = append(records, record)
	}
	s.sessions = map[string]*sessionRecord{}
	s.mu.Unlock()
	return records
}

func (s *Server) closeManagedSession(ctx context.Context, record *sessionRecord) error {
	if record == nil || !record.managed || record.session == nil {
		return nil
	}
	return record.session.Close(ctx)
}

func buildPrompt(req chatCompletionRequest, incremental bool) string {
	if incremental {
		for i := len(req.Messages) - 1; i >= 0; i-- {
			if req.Messages[i].Role == "user" {
				return req.Messages[i].Content.text()
			}
		}
		return req.Messages[len(req.Messages)-1].Content.text()
	}
	var out []string
	for _, msg := range req.Messages {
		text := strings.TrimSpace(msg.Content.text())
		if text == "" {
			continue
		}
		switch msg.Role {
		case "system":
			out = append(out, "[System]\n"+text)
		case "developer":
			out = append(out, "[Developer]\n"+text)
		case "assistant":
			out = append(out, "[Previous assistant response]\n"+text)
		case "tool":
			out = append(out, "[Tool result]\n"+text)
		default:
			out = append(out, text)
		}
	}
	return strings.Join(out, "\n\n")
}

func systemMessages(messages []chatMessage) []string {
	var out []string
	for _, msg := range messages {
		if msg.Role == "system" {
			if text := strings.TrimSpace(msg.Content.text()); text != "" {
				out = append(out, text)
			}
		}
	}
	return out
}

func completionResponse(req chatCompletionRequest, model string, completion acp.TurnCompletion) chatCompletionResponse {
	finish := finishReason(completion.StopReason)
	return chatCompletionResponse{
		ID:      newID("chatcmpl"),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   firstNonEmpty(req.Model, model),
		Choices: []chatCompletionChoice{{
			Index:        0,
			Message:      &choiceMessage{Role: "assistant", Content: completion.OutputText},
			FinishReason: &finish,
		}},
		Usage: usageFromACP(completion.Usage),
	}
}

func streamChunk(id string, model string, delta choiceDelta, finish *string, usage *openAIUsage) chatCompletionResponse {
	return chatCompletionResponse{
		ID:      id,
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []chatCompletionChoice{{
			Index:        0,
			Delta:        &delta,
			FinishReason: finish,
		}},
		Usage: usage,
	}
}

func validateChatCompletionRequest(req chatCompletionRequest) *requestValidationError {
	if req.N != nil && *req.N != 1 {
		return &requestValidationError{param: "n", message: "only n=1 is supported"}
	}
	for _, modality := range req.Modalities {
		if modality != "text" {
			return &requestValidationError{param: "modalities", message: "only text modality is supported"}
		}
	}
	if req.ResponseFormat != nil {
		switch req.ResponseFormat.Type {
		case "", "text":
		default:
			return &requestValidationError{param: "response_format", message: "only response_format.type=text is supported"}
		}
	}
	if len(req.Tools) > 0 && !toolChoiceIsNone(req.ToolChoice) {
		return &requestValidationError{param: "tools", message: "OpenAI tool calling is not supported by this gateway yet"}
	}
	return nil
}

func toolChoiceIsNone(value any) bool {
	if value == nil {
		return false
	}
	if text, ok := value.(string); ok {
		return text == "none"
	}
	if raw, ok := value.(json.RawMessage); ok {
		var text string
		return json.Unmarshal(raw, &text) == nil && text == "none"
	}
	return false
}

func usageFromACP(usage *acp.Usage) *openAIUsage {
	if usage == nil {
		return nil
	}
	return &openAIUsage{PromptTokens: usage.InputTokens, CompletionTokens: usage.OutputTokens, TotalTokens: usage.TotalTokens}
}

func finishReason(stopReason string) string {
	switch stopReason {
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	default:
		return "stop"
	}
}

func formatOperation(op *acp.Operation) string {
	if op == nil || op.Title == "" {
		return ""
	}
	return "\n\n[" + op.Phase + "] " + op.Title + "\n"
}

func formatPlan(entries []acp.PlanEntry) string {
	if len(entries) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nPlan:\n")
	for _, entry := range entries {
		b.WriteString("- ")
		if entry.Status != "" {
			b.WriteString(entry.Status)
			b.WriteString(": ")
		}
		b.WriteString(entry.Content)
		b.WriteString("\n")
	}
	return b.String()
}

func writeSSE(w http.ResponseWriter, flusher http.Flusher, value any) {
	data, _ := json.Marshal(value)
	fmt.Fprintf(w, "data: %s\n\n", data)
	flusher.Flush()
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message, param string) {
	if code == "" {
		code = "error"
	}
	writeJSON(w, status, openAIErrorResponse{Error: openAIError{Message: message, Type: code, Code: code, Param: param}})
}

type sessionHTTPError struct {
	status  int
	code    string
	message string
}

func (e sessionHTTPError) Error() string { return e.message }

func (s *Server) writeSessionError(w http.ResponseWriter, err error) {
	var sessionErr sessionHTTPError
	if errors.As(err, &sessionErr) {
		writeError(w, sessionErr.status, sessionErr.code, sessionErr.message, "")
		return
	}
	writeError(w, http.StatusBadGateway, "acp_session_error", err.Error(), "")
}

func (s *Server) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-API-Key, X-ACP-Session-ID, X-ACP-Session-Mode, X-ACP-Input-Mode, X-ACP-CWD")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if s.apiKey != "" {
			token := bearerToken(r.Header.Get("Authorization"))
			if token == "" {
				token = r.Header.Get("X-API-Key")
			}
			if token != s.apiKey {
				writeError(w, http.StatusUnauthorized, "invalid_api_key", "invalid API key", "")
				return
			}
		}
		next(w, r)
	}
}

func ownerHashFromRequest(r *http.Request) string {
	token := bearerToken(r.Header.Get("Authorization"))
	if token == "" {
		token = r.Header.Get("X-API-Key")
	}
	if token == "" {
		token = "anonymous"
	}
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func bearerToken(header string) string {
	const prefix = "Bearer "
	if strings.HasPrefix(header, prefix) {
		return strings.TrimSpace(strings.TrimPrefix(header, prefix))
	}
	return ""
}

func fingerprint(values ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(values, "\x00")))
	return hex.EncodeToString(sum[:])
}

func metadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	value, _ := metadata[key].(string)
	return strings.ToLower(strings.TrimSpace(value))
}

func newID(prefix string) string {
	var bytes [12]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	}
	return prefix + "_" + hex.EncodeToString(bytes[:])
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}
