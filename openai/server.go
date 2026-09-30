package openai

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	acp "github.com/saaskit-dev/acp-runtime-go"
)

const (
	headerSessionID       = "X-ACP-Session-ID"
	headerSessionMode     = "X-ACP-Session-Mode"
	headerInputMode       = "X-ACP-Input-Mode"
	headerCWD             = "X-ACP-CWD"
	headerSessionTTL      = "X-ACP-Session-TTL-Seconds"
	defaultSessionTTL     = 30 * time.Minute
	defaultCleanupPeriod  = time.Minute
	modelDiscoveryTimeout = 30 * time.Second
	defaultMaxSessions    = 256
)

type Config struct {
	Runtime                    *acp.Runtime
	ConnectionFactory          acp.ConnectionFactory
	DiscoveryConnectionFactory acp.ConnectionFactory
	RuntimeOptions             acp.RuntimeOptions
	DefaultAgentID             string
	ResolveAgent               func(context.Context, string) (acp.Agent, error)
	CWD                        string
	SessionTTL                 time.Duration
	// MaxSessions caps concurrent managed persistent sessions. Zero uses
	// defaultMaxSessions; negative disables the cap.
	MaxSessions int
	// MaxConcurrentSessions caps all live sessions and in-flight starts, including
	// temporary requests and discovery. Zero uses 256; negative disables the cap.
	MaxConcurrentSessions int
	// MaxOutputBytes bounds text delivered by one HTTP turn. Zero uses 8 MiB.
	// This is not a provider token, memory, or billing limit.
	MaxOutputBytes int
	// MaxResponseAliases bounds previous_response_id history. Zero uses 4096.
	MaxResponseAliases int
	// StreamWriteTimeout bounds each SSE write on writers supporting deadlines.
	// Zero uses 30 seconds; negative disables the deadline.
	StreamWriteTimeout time.Duration
	APIKey             string
	AllowHeaderCWD     bool
	Models             []string
	Agents             []string
	DiscoverModels     bool
	ModelDiscoveryTTL  time.Duration
	// AccessLog receives one line per HTTP request when non-nil.
	AccessLog func(AccessLogEntry)
}

// AccessLogEntry is a single HTTP request summary for gateway operators.
type AccessLogEntry struct {
	Method     string
	Path       string
	Status     int
	Duration   time.Duration
	SessionID  string
	AgentID    string
	Error      string
	RemoteAddr string
}

type Server struct {
	runtime               *acp.Runtime
	discoveryRuntime      *acp.Runtime
	ownsRuntime           bool
	ownsDiscoveryRuntime  bool
	ctx                   context.Context
	cancel                context.CancelFunc
	defaultAgentID        string
	cwd                   string
	sessionTTL            time.Duration
	maxSessions           int
	maxConcurrentSessions int
	maxOutputBytes        int
	maxResponseAliases    int
	streamWriteTimeout    time.Duration
	apiKey                string
	allowHeaderCWD        bool
	models                []string
	agents                []string
	discoverModels        bool
	discoveryTTL          time.Duration
	resolveAgent          func(context.Context, string) (acp.Agent, error)
	accessLog             func(AccessLogEntry)

	mu                sync.Mutex
	sessions          map[string]*sessionRecord
	responses         map[string]string
	done              chan struct{}
	closeOnce         sync.Once
	cleanupMu         sync.Mutex
	closed            bool
	pendingPersistent int
	pendingStarts     int
	liveSessions      map[*acp.Session]struct{}
	responseOrder     []string

	modelMu          sync.Mutex
	modelCache       []string
	modelCacheExpiry time.Time
}

type sessionRecord struct {
	id          string
	acpID       string
	session     *acp.Session
	managed     bool
	agentID     string
	modelID     string
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
	closed  bool
}

type requestContext struct {
	ownerHash   string
	agentID     string
	modelID     string
	effort      string
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
	maxSessions := config.MaxSessions
	if maxSessions == 0 {
		maxSessions = defaultMaxSessions
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
	if len(models) == 0 && !config.DiscoverModels {
		models = []string{defaultAgentID}
	}
	models = uniqueStrings(models)
	agents := append([]string(nil), config.Agents...)
	if len(agents) == 0 {
		agents = []string{defaultAgentID}
	}
	agents = uniqueStrings(agents)
	discoveryTTL := config.ModelDiscoveryTTL
	if discoveryTTL <= 0 {
		discoveryTTL = 10 * time.Minute
	}
	discoveryRuntime := runtime
	ownsDiscoveryRuntime := false
	if config.DiscoverModels && config.DiscoveryConnectionFactory != nil {
		discoveryRuntime = acp.NewRuntime(config.DiscoveryConnectionFactory, config.RuntimeOptions)
		ownsDiscoveryRuntime = true
	}
	serverCtx, cancel := context.WithCancel(context.Background())
	server := &Server{
		runtime:               runtime,
		discoveryRuntime:      discoveryRuntime,
		ownsRuntime:           ownsRuntime,
		ownsDiscoveryRuntime:  ownsDiscoveryRuntime,
		ctx:                   serverCtx,
		cancel:                cancel,
		defaultAgentID:        defaultAgentID,
		cwd:                   cwd,
		sessionTTL:            ttl,
		maxSessions:           maxSessions,
		maxConcurrentSessions: config.MaxConcurrentSessions,
		maxOutputBytes:        config.MaxOutputBytes,
		maxResponseAliases:    config.MaxResponseAliases,
		streamWriteTimeout:    config.StreamWriteTimeout,
		liveSessions:          map[*acp.Session]struct{}{},
		apiKey:                config.APIKey,
		allowHeaderCWD:        config.AllowHeaderCWD,
		models:                models,
		agents:                agents,
		discoverModels:        config.DiscoverModels,
		discoveryTTL:          discoveryTTL,
		resolveAgent:          config.ResolveAgent,
		accessLog:             config.AccessLog,
		sessions:              map[string]*sessionRecord{},
		responses:             map[string]string{},
		done:                  make(chan struct{}),
	}
	if server.maxConcurrentSessions == 0 {
		server.maxConcurrentSessions = defaultMaxSessions
	}
	if server.maxOutputBytes <= 0 {
		server.maxOutputBytes = 8 * 1024 * 1024
	}
	if server.maxResponseAliases <= 0 {
		server.maxResponseAliases = 4096
	}
	if server.streamWriteTimeout == 0 {
		server.streamWriteTimeout = 30 * time.Second
	}
	if server.resolveAgent == nil {
		server.resolveAgent = acp.ResolveRuntimeAgentFromRegistry
	}
	go server.cleanupLoop()
	go server.prewarmModelCache()
	return server
}

func (s *Server) Close(ctx context.Context) error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		if s.done != nil {
			close(s.done)
		}
		if s.cancel != nil {
			s.cancel()
		}
	})
	s.cleanupMu.Lock()
	defer s.cleanupMu.Unlock()
	var firstErr error
	for _, record := range s.drainSessions() {
		if err := s.closeManagedSession(ctx, record); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	s.mu.Lock()
	live := make([]*acp.Session, 0, len(s.liveSessions))
	for session := range s.liveSessions {
		live = append(live, session)
	}
	s.mu.Unlock()
	for _, session := range live {
		if err := s.closeSession(ctx, session); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if s.ownsRuntime && s.runtime != nil {
		if err := s.runtime.Close(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if s.ownsDiscoveryRuntime && s.discoveryRuntime != nil {
		if err := s.discoveryRuntime.Close(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/ready", s.handleReady)
	mux.HandleFunc("/v1/models", s.withAuth(s.handleModels))
	mux.HandleFunc("/v1/responses", s.withAuth(s.handleResponses))
	mux.HandleFunc("/v1/chat/completions", s.withAuth(s.handleChatCompletions))
	mux.HandleFunc("/v1/acp/sessions", s.withAuth(s.handleSessions))
	mux.HandleFunc("/v1/acp/sessions/", s.withAuth(s.handleSessionByID))
	return s.withAccessLog(mux)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", "")
		return
	}
	// Liveness: process is up.
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", "")
		return
	}
	sessions, busy := s.sessionStats()
	writeJSON(w, http.StatusOK, map[string]any{
		"status":        "ready",
		"sessions":      sessions,
		"busy":          busy,
		"max_sessions":  s.maxSessions,
		"session_ttl":   s.sessionTTL.String(),
		"default_agent": s.defaultAgentID,
	})
}

func (s *Server) sessionStats() (total, busy int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	total = len(s.sessions)
	for _, record := range s.sessions {
		if record.isBusy() {
			busy++
		}
	}
	return total, busy
}

func (s *Server) withAccessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.accessLog == nil {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		s.accessLog(AccessLogEntry{
			Method:     r.Method,
			Path:       r.URL.Path,
			Status:     rec.status,
			Duration:   time.Since(start),
			SessionID:  r.Header.Get(headerSessionID),
			RemoteAddr: r.RemoteAddr,
		})
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// FlushError lets ResponseController retain network/flush errors through the
// access-log wrapper; Unwrap preserves write-deadline support.
func (r *statusRecorder) FlushError() error {
	return http.NewResponseController(r.ResponseWriter).Flush()
}

func (r *statusRecorder) Flush() { _ = r.FlushError() }

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", "")
		return
	}
	now := time.Now().Unix()
	models := s.modelsForList(r.Context())
	models = publicModelIDs(models)
	data := make([]modelObject, 0, len(models))
	for _, model := range models {
		data = append(data, modelObject{ID: model, Object: "model", Created: now, OwnedBy: "acp"})
	}
	writeJSON(w, http.StatusOK, modelListResponse{Object: "list", Data: data})
}

func (s *Server) modelsForList(ctx context.Context) []string {
	base := append([]string(nil), s.models...)
	if !s.discoverModels {
		return uniqueStrings(base)
	}
	now := time.Now()
	s.modelMu.Lock()
	if now.Before(s.modelCacheExpiry) {
		cached := append([]string(nil), s.modelCache...)
		s.modelMu.Unlock()
		return uniqueStrings(append(base, cached...))
	}
	s.modelMu.Unlock()

	discovered := s.discoverModelIDs(ctx)
	s.modelMu.Lock()
	s.modelCache = append([]string(nil), discovered...)
	s.modelCacheExpiry = now.Add(s.discoveryTTL)
	s.modelMu.Unlock()

	out := uniqueStrings(append(base, discovered...))
	if len(out) == 0 {
		return []string{s.defaultAgentID}
	}
	return out
}

func (s *Server) prewarmModelCache() {
	if !s.discoverModels {
		return
	}
	_ = s.modelsForList(s.ctx)
}

func (s *Server) discoverModelIDs(ctx context.Context) []string {
	var out []string
	for _, agentID := range s.agents {
		models, err := s.discoverAgentModels(ctx, agentID)
		if err != nil {
			continue
		}
		for _, model := range models {
			model = strings.TrimSpace(model)
			if model == "" {
				continue
			}
			out = append(out, agentID+"/"+model)
		}
	}
	return uniqueStrings(out)
}

func (s *Server) discoverAgentModels(ctx context.Context, agentID string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, modelDiscoveryTimeout)
	defer cancel()
	agent, err := s.resolveAgent(ctx, agentID)
	if err != nil {
		return nil, err
	}
	runtime := s.discoveryRuntime
	if runtime == nil {
		runtime = s.runtime
	}
	session, err := s.startTrackedSession(ctx, runtime, acp.StartSessionOptions{Agent: agent, CWD: s.cwd})
	if err != nil {
		return nil, err
	}
	defer s.closeSession(context.Background(), session)
	return modelIDsFromMetadata(session.Metadata()), nil
}

func modelIDsFromMetadata(metadata acp.RuntimeSessionMetadata) []string {
	var out []string
	for _, model := range metadata.AgentModels {
		if strings.TrimSpace(model.ID) != "" {
			out = append(out, strings.TrimSpace(model.ID))
		}
	}
	if len(out) > 0 {
		return uniqueStrings(out)
	}
	for _, option := range metadata.AgentConfigOptions {
		if !isModelConfigOption(option) {
			continue
		}
		for _, choice := range option.Options {
			if value, ok := choice.Value.(string); ok && strings.TrimSpace(value) != "" {
				out = append(out, strings.TrimSpace(value))
			}
		}
	}
	return uniqueStrings(out)
}

func isModelConfigOption(option acp.RuntimeAgentConfigOption) bool {
	return strings.EqualFold(option.ID, "model") || strings.EqualFold(option.Category, "model")
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
				Model:        record.modelID,
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
		s.removeSession(record)
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
	if err := decoder.Decode(new(any)); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "request body must contain one JSON object", "")
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
	if err := s.validateModelRoute(ctx, req.Model, rc); err != nil {
		writeError(w, err.status, err.code, err.message, "model")
		return
	}
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
			s.writeSessionError(w, err)
			return
		}
		defer s.closeSession(context.Background(), temporary)
	}

	incremental := sessionID != "" && strings.ToLower(strings.TrimSpace(r.Header.Get(headerInputMode))) != "replay"
	if req.Stream {
		if persistent {
			s.streamPersistent(ctx, w, record, req, incremental)
			return
		}
		s.streamTemporary(ctx, w, temporary, req, firstNonEmpty(req.Model, rc.responseModel()))
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
		s.writeTurnError(w, err)
		return
	}
	if err := s.checkOutput(completion.OutputText); err != nil {
		s.writeTurnError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, completionResponse(req, rc.responseModel(), completion))
}

func (s *Server) handleResponses(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", "")
		return
	}
	var req responseRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8*1024*1024))
	if err := decoder.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid JSON request: "+err.Error(), "")
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "request body must contain one JSON object", "")
		return
	}
	if err := validateResponseRequest(req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", err.Error(), err.param)
		return
	}
	if req.Input.empty() {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "input must not be empty", "input")
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	rc := s.buildResponseRequestContext(r, req)
	if err := s.validateModelRoute(ctx, req.Model, rc); err != nil {
		writeError(w, err.status, err.code, err.message, "model")
		return
	}
	sessionID := strings.TrimSpace(r.Header.Get(headerSessionID))
	if req.PreviousResponseID != "" {
		var ok bool
		sessionID, ok = s.sessionIDForResponse(req.PreviousResponseID)
		if !ok {
			writeError(w, http.StatusNotFound, "response_not_found", "previous response not found", "previous_response_id")
			return
		}
	}
	store := req.Store == nil || *req.Store
	persistent := store || sessionID != ""

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
			s.writeSessionError(w, err)
			return
		}
		defer s.closeSession(context.Background(), temporary)
	}

	responseID := newID("resp")
	if persistent && store {
		s.registerResponseSession(responseID, record)
	}

	incremental := req.PreviousResponseID != "" || sessionID != ""
	if req.Stream {
		if persistent {
			s.streamResponsePersistent(ctx, w, record, req, responseID, incremental)
			return
		}
		s.streamResponseTemporary(ctx, w, temporary, req, responseID, firstNonEmpty(req.Model, rc.responseModel()))
		return
	}

	var completion acp.TurnCompletion
	if persistent {
		completion, err = s.runResponsePersistent(ctx, record, req, incremental)
	} else {
		completion, err = temporary.Run(ctx, buildResponsePrompt(req, false))
	}
	if err != nil {
		s.writeTurnError(w, err)
		return
	}
	if err := s.checkOutput(completion.OutputText); err != nil {
		s.writeTurnError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, responseFromCompletion(responseID, firstNonEmpty(req.Model, rc.responseModel()), completion, req.Metadata))
}

func (s *Server) buildRequestContext(r *http.Request, req chatCompletionRequest) requestContext {
	agentID, modelID := s.resolveModelRoute(req.Model)
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
	fingerprint := fingerprint(agentID, modelID, cwd)
	return requestContext{
		ownerHash:   ownerHashFromRequest(r),
		agentID:     agentID,
		modelID:     modelID,
		cwd:         cwd,
		fingerprint: fingerprint,
		systemHash:  systemHash,
	}
}

func (s *Server) buildResponseRequestContext(r *http.Request, req responseRequest) requestContext {
	agentID, modelID := s.resolveModelRoute(req.Model)
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
	systemHash := ""
	if instructions := strings.TrimSpace(req.Instructions); instructions != "" {
		systemHash = fingerprint(instructions)
	}
	effort := responseReasoningEffort(req.Reasoning)
	fingerprint := fingerprint(agentID, modelID, effort, cwd)
	return requestContext{
		ownerHash:   ownerHashFromRequest(r),
		agentID:     agentID,
		modelID:     modelID,
		effort:      effort,
		cwd:         cwd,
		fingerprint: fingerprint,
		systemHash:  systemHash,
	}
}

func (s *Server) resolveModelRoute(model string) (string, string) {
	agentID := s.defaultAgentID
	modelID := strings.TrimSpace(model)
	if strings.Contains(modelID, "/") {
		parts := strings.SplitN(modelID, "/", 2)
		agentID = strings.TrimSpace(parts[0])
		modelID = strings.TrimSpace(parts[1])
	} else if routedAgent, routedModel, ok := s.resolvePublicModel(modelID); ok {
		agentID = routedAgent
		modelID = routedModel
	} else if inferred := s.inferAgentForBareModel(modelID); inferred != "" {
		agentID = inferred
	}
	return agentID, modelID
}

func (s *Server) resolvePublicModel(model string) (string, string, bool) {
	model = strings.TrimSpace(model)
	if model == "" {
		return "", "", false
	}
	for _, internal := range s.modelsForList(s.ctx) {
		if !strings.Contains(internal, "/") {
			continue
		}
		public, ok := publicModelID(internal)
		if !ok || !publicModelMatches(public, model) {
			continue
		}
		agentID, modelID, ok := splitInternalModelID(internal)
		if !ok {
			continue
		}
		return agentID, cleanInternalModelName(modelID), true
	}
	if containsString(s.models, model) {
		return s.defaultAgentID, model, true
	}
	return "", "", false
}

func (s *Server) inferAgentForBareModel(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return ""
	}
	preferred := ""
	lower := strings.ToLower(model)
	switch {
	case strings.HasPrefix(lower, "claude"):
		preferred = "claude"
	case strings.HasPrefix(lower, "gpt"), strings.HasPrefix(lower, "o1"), strings.HasPrefix(lower, "o3"), strings.HasPrefix(lower, "o4"), strings.HasPrefix(lower, "o5"):
		preferred = "codex"
	default:
		return ""
	}
	if s.modelRouteExists(preferred, model) || containsString(s.agents, preferred) {
		return preferred
	}
	return ""
}

func (s *Server) modelRouteExists(agentID string, model string) bool {
	route := agentID + "/" + model
	if containsString(s.models, route) {
		return true
	}
	s.modelMu.Lock()
	defer s.modelMu.Unlock()
	return containsString(s.modelCache, route)
}

func (s *Server) validateModelRoute(ctx context.Context, requestedModel string, rc requestContext) *sessionHTTPError {
	if strings.TrimSpace(requestedModel) == "" {
		return nil
	}
	available := s.modelsForList(ctx)
	if !hasModelInventory(available) {
		return nil
	}
	canonical := rc.agentID + "/" + rc.modelID
	if strings.Contains(strings.TrimSpace(requestedModel), "/") {
		canonical = strings.TrimSpace(requestedModel)
	}
	if containsString(available, requestedModel) || internalModelAvailable(available, canonical) {
		return nil
	}
	message := fmt.Sprintf("model %q is not available for agent %q", requestedModel, rc.agentID)
	return &sessionHTTPError{status: http.StatusNotFound, code: "model_not_found", message: message}
}

func hasModelInventory(models []string) bool {
	for _, model := range models {
		if strings.Contains(strings.TrimSpace(model), "/") {
			return true
		}
	}
	return false
}

func publicModelIDs(internal []string) []string {
	var out []string
	for _, model := range internal {
		public, ok := publicModelID(model)
		if ok {
			out = append(out, public)
		}
	}
	return uniqueStrings(out)
}

func publicModelID(internal string) (string, bool) {
	agentID, modelID, ok := splitInternalModelID(internal)
	if !ok {
		modelID = cleanInternalModelName(internal)
		if modelID == "" || strings.EqualFold(modelID, "default") {
			return "", false
		}
		return modelID, true
	}
	modelID = cleanInternalModelName(modelID)
	if modelID == "" || strings.EqualFold(modelID, "default") {
		return "", false
	}
	switch strings.ToLower(agentID) {
	case "codex":
		return modelID, true
	case "claude":
		return claudePublicModelID(modelID)
	default:
		return modelID, true
	}
}

func claudePublicModelID(modelID string) (string, bool) {
	switch strings.ToLower(cleanInternalModelName(modelID)) {
	case "opus", "opus-4", "opus-4-8", "claude-opus-4-8":
		return "claude-opus-4-8", true
	case "sonnet", "sonnet-4", "sonnet-4-6", "claude-sonnet-4-6":
		return "claude-sonnet-4-6", true
	case "haiku", "haiku-4-5", "haiku-4-5-20251001", "claude-haiku-4-5", "claude-haiku-4-5-20251001":
		return "claude-haiku-4-5-20251001", true
	default:
		if strings.HasPrefix(strings.ToLower(modelID), "claude-") {
			return cleanInternalModelName(modelID), true
		}
		return "", false
	}
}

func publicModelMatches(public string, requested string) bool {
	if public == requested {
		return true
	}
	return public == "claude-haiku-4-5-20251001" && requested == "claude-haiku-4-5"
}

func splitInternalModelID(model string) (string, string, bool) {
	model = strings.TrimSpace(model)
	if !strings.Contains(model, "/") {
		return "", "", false
	}
	parts := strings.SplitN(model, "/", 2)
	agentID := strings.TrimSpace(parts[0])
	modelID := strings.TrimSpace(parts[1])
	return agentID, modelID, agentID != "" && modelID != ""
}

func cleanInternalModelName(model string) string {
	model = strings.TrimSpace(model)
	if i := strings.Index(model, "["); i >= 0 {
		model = strings.TrimSpace(model[:i])
	}
	return model
}

func internalModelAvailable(available []string, canonical string) bool {
	canonicalAgent, canonicalModel, ok := splitInternalModelID(canonical)
	if !ok {
		return containsString(available, canonical)
	}
	canonicalModel = cleanInternalModelName(canonicalModel)
	for _, model := range available {
		agentID, modelID, ok := splitInternalModelID(model)
		if !ok {
			continue
		}
		if agentID == canonicalAgent && cleanInternalModelName(modelID) == canonicalModel {
			return true
		}
	}
	return false
}

func (rc requestContext) responseModel() string {
	return firstNonEmpty(rc.modelID, rc.agentID)
}

func (r *sessionRecord) responseModel() string {
	return firstNonEmpty(r.modelID, r.agentID)
}

func (s *Server) startSession(ctx context.Context, rc requestContext) (*acp.Session, error) {
	agent, err := s.resolveAgent(ctx, rc.agentID)
	if err != nil {
		return nil, err
	}
	options := acp.StartSessionOptions{Agent: agent, CWD: rc.cwd}
	if rc.modelID != "" {
		options.InitialConfig.Model = rc.modelID
	}
	if rc.effort != "" {
		options.InitialConfig.Effort = rc.effort
	}
	return s.startTrackedSession(ctx, s.runtime, options)
}

func (s *Server) createPersistentSession(ctx context.Context, rc requestContext) (*sessionRecord, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, serverClosedError()
	}
	if s.maxSessions > 0 && len(s.sessions)+s.pendingPersistent >= s.maxSessions {
		s.mu.Unlock()
		return nil, sessionHTTPError{status: http.StatusTooManyRequests, code: "session_limit", message: "maximum persistent sessions reached"}
	}
	s.pendingPersistent++
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.pendingPersistent--; s.mu.Unlock() }()
	session, err := s.startSession(ctx, rc)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	record := &sessionRecord{
		id: newID("acpsess"), acpID: session.Snapshot().Session.ID,
		session: session, managed: true, agentID: rc.agentID, modelID: rc.modelID,
		cwd: rc.cwd, ownerHash: rc.ownerHash, fingerprint: rc.fingerprint,
		systemHash: rc.systemHash, createdAt: now, lastSeenAt: now, expiresAt: now.Add(s.sessionTTL),
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = s.closeSession(context.Background(), session)
		return nil, serverClosedError()
	}
	s.sessions[record.id] = record
	s.mu.Unlock()
	return record, nil
}

func (s *Server) validatePersistentSession(id string, rc requestContext) (*sessionRecord, error) {
	s.mu.Lock()
	record, ok := s.sessions[id]
	if !ok || record.ownerHash != rc.ownerHash {
		s.mu.Unlock()
		return nil, sessionHTTPError{status: http.StatusNotFound, code: "session_not_found", message: "ACP session not found"}
	}
	record.mu.Lock()
	var err error
	switch {
	case record.fingerprint != rc.fingerprint:
		err = sessionHTTPError{status: http.StatusConflict, code: "session_conflict", message: "ACP session fingerprint does not match request"}
	case rc.systemHash != "" && record.systemHash != "" && rc.systemHash != record.systemHash:
		err = sessionHTTPError{status: http.StatusConflict, code: "session_conflict", message: "ACP session system prompt does not match"}
	default:
		err = record.availabilityLocked(time.Now())
	}
	expired := isSessionError(err, "session_expired")
	if expired {
		s.removeSessionLocked(record)
	}
	record.mu.Unlock()
	s.mu.Unlock()
	if expired {
		_ = s.closeManagedSession(context.Background(), record)
	}
	if err != nil {
		return nil, err
	}
	return record, nil
}

// beginSession repeats identity and idle-expiry checks while acquiring the lease.
// No stale pointer may start after cleanup/delete/replacement removed its record.
func (s *Server) beginSession(record *sessionRecord) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return serverClosedError()
	}
	if s.sessions[record.id] != record {
		s.mu.Unlock()
		return sessionHTTPError{status: http.StatusGone, code: "session_expired", message: "ACP session is no longer available"}
	}
	record.mu.Lock()
	err := record.availabilityLocked(time.Now())
	expired := isSessionError(err, "session_expired")
	if expired {
		s.removeSessionLocked(record)
	}
	if err == nil {
		record.beginLocked(s.sessionTTL)
	}
	record.mu.Unlock()
	s.mu.Unlock()
	if expired {
		_ = s.closeManagedSession(context.Background(), record)
	}
	return err
}

func (s *Server) runPersistent(ctx context.Context, record *sessionRecord, req chatCompletionRequest, incremental bool) (acp.TurnCompletion, error) {
	if err := s.beginSession(record); err != nil {
		return acp.TurnCompletion{}, err
	}
	defer s.endSession(record, ctx)
	prompt := buildPrompt(req, incremental)
	return record.session.Run(ctx, prompt)
}

func (s *Server) streamPersistent(ctx context.Context, w http.ResponseWriter, record *sessionRecord, req chatCompletionRequest, incremental bool) {
	if err := s.beginSession(record); err != nil {
		s.writeSessionError(w, err)
		return
	}
	defer s.endSession(record, ctx)
	prompt := buildPrompt(req, incremental)
	if err := s.streamTurn(ctx, w, record.session, prompt, req, firstNonEmpty(req.Model, record.responseModel())); err != nil {
		record.end(true, s.sessionTTL)
	}
}

func (s *Server) streamTemporary(ctx context.Context, w http.ResponseWriter, session *acp.Session, req chatCompletionRequest, model string) {
	prompt := buildPrompt(req, false)
	s.streamTurn(ctx, w, session, prompt, req, model)
}

func (s *Server) runResponsePersistent(ctx context.Context, record *sessionRecord, req responseRequest, incremental bool) (acp.TurnCompletion, error) {
	if err := s.beginSession(record); err != nil {
		return acp.TurnCompletion{}, err
	}
	defer s.endSession(record, ctx)
	return record.session.Run(ctx, buildResponsePrompt(req, incremental))
}

func (s *Server) streamResponsePersistent(ctx context.Context, w http.ResponseWriter, record *sessionRecord, req responseRequest, responseID string, incremental bool) {
	if err := s.beginSession(record); err != nil {
		s.writeSessionError(w, err)
		return
	}
	defer s.endSession(record, ctx)
	if err := s.streamResponseTurn(ctx, w, record.session, buildResponsePrompt(req, incremental), req, responseID, firstNonEmpty(req.Model, record.responseModel())); err != nil {
		record.end(true, s.sessionTTL)
	}
}

func (s *Server) streamResponseTemporary(ctx context.Context, w http.ResponseWriter, session *acp.Session, req responseRequest, responseID string, model string) {
	s.streamResponseTurn(ctx, w, session, buildResponsePrompt(req, false), req, responseID, model)
}

// availabilityLocked checks idle TTL only after busy. TTL is not a run deadline.
func (r *sessionRecord) availabilityLocked(now time.Time) error {
	switch {
	case r.closed:
		return sessionHTTPError{status: http.StatusGone, code: "session_expired", message: "ACP session is closed"}
	case r.tainted:
		return sessionHTTPError{status: http.StatusConflict, code: "session_tainted", message: "ACP session is tainted; create a new session"}
	case r.busy:
		return sessionHTTPError{status: http.StatusConflict, code: "session_busy", message: "ACP session is busy"}
	case !r.expiresAt.IsZero() && !now.Before(r.expiresAt):
		return sessionHTTPError{status: http.StatusGone, code: "session_expired", message: "ACP session expired"}
	default:
		return nil
	}
}

func (r *sessionRecord) beginLocked(ttl time.Duration) {
	r.busy = true
	r.lastSeenAt = time.Now()
	if ttl > 0 {
		r.expiresAt = r.lastSeenAt.Add(ttl)
	}
}

func (r *sessionRecord) tryBegin(ttl time.Duration) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.availabilityLocked(time.Now()) != nil {
		return false
	}
	r.beginLocked(ttl)
	return true
}

func (s *Server) endSession(record *sessionRecord, ctx context.Context) {
	status := ""
	if record.session != nil {
		status = record.session.Status()
	}
	record.end(ctx.Err() != nil || status == "tainted" || status == "closed", s.sessionTTL)
}

func (r *sessionRecord) end(tainted bool, ttl time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.busy = false
	r.tainted = r.tainted || tainted
	if !r.closed {
		r.lastSeenAt = time.Now()
		r.expiresAt = r.lastSeenAt.Add(ttl)
	}
}

func (r *sessionRecord) isBusy() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.busy
}

func (s *Server) getSession(id string) (*sessionRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.sessions[id]
	return record, ok
}

// All operations involving both locks take s.mu before record.mu. Closing a
// transport is always outside these locks, and deletion checks pointer identity.
func (s *Server) removeSession(record *sessionRecord) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	record.mu.Lock()
	defer record.mu.Unlock()
	return s.removeSessionLocked(record)
}

func (s *Server) removeSessionLocked(record *sessionRecord) bool {
	if s.sessions[record.id] != record {
		return false
	}
	record.closed = true
	delete(s.sessions, record.id)
	s.removeResponseAliasesLocked(record.id)
	return true
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
	for _, record := range s.sessions {
		record.mu.Lock()
		if !record.busy && !record.expiresAt.IsZero() && !now.Before(record.expiresAt) {
			if s.removeSessionLocked(record) {
				expired = append(expired, record)
			}
		}
		record.mu.Unlock()
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
		record.mu.Lock()
		record.closed = true
		record.mu.Unlock()
		records = append(records, record)
	}
	s.sessions = map[string]*sessionRecord{}
	s.responses = map[string]string{}
	s.responseOrder = nil
	s.mu.Unlock()
	return records
}

func (s *Server) registerResponseSession(responseID string, record *sessionRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessions[record.id] != record {
		return
	}
	limit := s.maxResponseAliases
	if limit <= 0 {
		limit = 4096
	}
	for len(s.responseOrder) >= limit {
		delete(s.responses, s.responseOrder[0])
		s.responseOrder = s.responseOrder[1:]
	}
	s.responses[responseID] = record.id
	s.responseOrder = append(s.responseOrder, responseID)
}

func (s *Server) sessionIDForResponse(responseID string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sessionID, ok := s.responses[responseID]
	return sessionID, ok
}

func (s *Server) removeResponseAliasesLocked(sessionID string) {
	for responseID, id := range s.responses {
		if id == sessionID {
			delete(s.responses, responseID)
		}
	}
	order := s.responseOrder[:0]
	for _, id := range s.responseOrder {
		if _, ok := s.responses[id]; ok {
			order = append(order, id)
		}
	}
	s.responseOrder = order
}

func (s *Server) closeManagedSession(ctx context.Context, record *sessionRecord) error {
	if record == nil || !record.managed || record.session == nil {
		return nil
	}
	return s.closeSession(ctx, record.session)
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

func buildResponsePrompt(req responseRequest, incremental bool) string {
	var out []string
	if instructions := strings.TrimSpace(req.Instructions); instructions != "" {
		out = append(out, "[Instructions]\n"+instructions)
	}
	if incremental {
		if text := strings.TrimSpace(req.Input.text()); text != "" {
			out = append(out, text)
		}
		return strings.Join(out, "\n\n")
	}
	if text := strings.TrimSpace(req.Input.text()); text != "" {
		out = append(out, text)
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

func responseFromCompletion(id string, model string, completion acp.TurnCompletion, metadata map[string]any) responseObject {
	text := completion.OutputText
	return responseObject{
		ID:         id,
		Object:     "response",
		CreatedAt:  time.Now().Unix(),
		Status:     "completed",
		Model:      model,
		OutputText: text,
		Output: []responseOutputItem{{
			ID:     newID("msg"),
			Type:   "message",
			Status: "completed",
			Role:   "assistant",
			Content: []responseOutputContent{{
				Type:        "output_text",
				Text:        text,
				Annotations: []any{},
			}},
		}},
		Usage:    responseUsageFromACP(completion.Usage),
		Metadata: metadata,
	}
}

func responseSkeleton(id string, model string, created int64, status string, metadata map[string]any) responseObject {
	return responseObject{
		ID:        id,
		Object:    "response",
		CreatedAt: created,
		Status:    status,
		Model:     model,
		Output:    []responseOutputItem{},
		Metadata:  metadata,
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
	if req.MaxTokens != nil {
		return unsupportedParameter("max_tokens")
	}
	if req.MaxCompletionTokens != nil {
		return unsupportedParameter("max_completion_tokens")
	}
	if err := validateSampling(req.Temperature, req.TopP); err != nil {
		return err
	}
	if !emptyStop(req.Stop) {
		return unsupportedParameter("stop")
	}
	for i, message := range req.Messages {
		for j, part := range message.Content.Parts {
			if part.Type != "text" && part.Type != "input_text" {
				return unsupportedParameter(fmt.Sprintf("messages[%d].content[%d].type", i, j))
			}
		}
	}
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
	if (len(req.Tools) > 0 || req.ToolChoice != nil) && !toolChoiceIsNone(req.ToolChoice) {
		return &requestValidationError{param: "tools", message: "OpenAI tool calling is not supported by this gateway yet"}
	}
	return nil
}

func validateResponseRequest(req responseRequest) *requestValidationError {
	if req.MaxOutputTokens != nil {
		return unsupportedParameter("max_output_tokens")
	}
	if err := validateSampling(req.Temperature, req.TopP); err != nil {
		return err
	}
	if !emptyStop(req.Stop) {
		return unsupportedParameter("stop")
	}
	for i, item := range req.Input.Items {
		if item.Type != "" && item.Type != "message" {
			return unsupportedParameter(fmt.Sprintf("input[%d].type", i))
		}
		for j, part := range item.Content.Parts {
			if part.Type != "text" && part.Type != "input_text" && part.Type != "output_text" {
				return unsupportedParameter(fmt.Sprintf("input[%d].content[%d].type", i, j))
			}
		}
	}
	if req.Reasoning != nil && req.Reasoning.Summary != "" {
		return unsupportedParameter("reasoning.summary")
	}
	if req.Text != nil && req.Text.Format != nil {
		switch req.Text.Format.Type {
		case "", "text":
		default:
			return &requestValidationError{param: "text.format", message: "only text.format.type=text is supported"}
		}
	}
	if (len(req.Tools) > 0 || req.ToolChoice != nil) && !toolChoiceIsNone(req.ToolChoice) {
		return &requestValidationError{param: "tools", message: "OpenAI tool calling is not supported by this gateway yet"}
	}
	if req.Store != nil && !*req.Store && strings.TrimSpace(req.PreviousResponseID) != "" {
		return &requestValidationError{param: "store", message: "store=false cannot be used with previous_response_id"}
	}
	if effort := responseReasoningEffort(req.Reasoning); effort != "" && !validReasoningEffort(effort) {
		return &requestValidationError{param: "reasoning.effort", message: "unsupported reasoning.effort"}
	}
	return nil
}

func responseReasoningEffort(reasoning *responseReasoningConfig) string {
	if reasoning == nil {
		return ""
	}
	return strings.TrimSpace(reasoning.Effort)
}

func validReasoningEffort(effort string) bool {
	switch effort {
	case "none", "low", "medium", "high", "xhigh", "max":
		return true
	default:
		return false
	}
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

func responseUsageFromACP(usage *acp.Usage) *responseUsage {
	if usage == nil {
		return nil
	}
	return &responseUsage{InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens, TotalTokens: usage.TotalTokens}
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
	e := classifyError(err, "acp_session_error")
	writeError(w, e.status, e.code, e.message, "")
}

func (s *Server) writeTurnError(w http.ResponseWriter, err error) {
	e := classifyError(err, "acp_turn_error")
	writeError(w, e.status, e.code, e.message, "")
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
	return strings.ToLower(metadataValue(metadata, key))
}

func metadataValue(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
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

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
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
