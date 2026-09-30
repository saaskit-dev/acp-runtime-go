package acpruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/mail"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// ElicitationHandlers are connection-level, since request-scoped interaction
// can precede session creation. Installing a mode promises an actual host UI:
// show the requesting agent/message, review, decline and cancel controls; URL
// handlers must show the full URL and obtain consent before secure navigation.
// The runtime never opens or fetches URLs and never requests credentials.
type ElicitationHandlers struct {
	Form       ElicitationHandler
	URL        ElicitationHandler
	OnComplete func(Context, ElicitationCompletion)
	Timeout    time.Duration
}
type ElicitationHandler func(Context, ElicitationRequest) (ElicitationResponse, error)
type ElicitationRequest struct {
	Mode            string             `json:"mode"`
	Message         string             `json:"message"`
	SessionID       string             `json:"sessionId,omitempty"`
	ToolCallID      *string            `json:"toolCallId,omitempty"`
	RequestID       json.RawMessage    `json:"requestId,omitempty"`
	RequestedSchema *ElicitationSchema `json:"requestedSchema,omitempty"`
	ElicitationID   string             `json:"elicitationId,omitempty"`
	URL             string             `json:"url,omitempty"`
	Meta            map[string]any     `json:"_meta,omitempty"`
}
type ElicitationSchema struct {
	Type        string                         `json:"type,omitempty"`
	Title       string                         `json:"title,omitempty"`
	Description string                         `json:"description,omitempty"`
	Properties  map[string]ElicitationProperty `json:"properties,omitempty"`
	Required    []string                       `json:"required,omitempty"`
	Meta        map[string]any                 `json:"_meta,omitempty"`
}
type ElicitationProperty struct {
	Type        string                  `json:"type"`
	Title       string                  `json:"title,omitempty"`
	Description string                  `json:"description,omitempty"`
	Format      string                  `json:"format,omitempty"`
	Pattern     string                  `json:"pattern,omitempty"`
	Default     json.RawMessage         `json:"default,omitempty"`
	Enum        []string                `json:"enum,omitempty"`
	OneOf       []ElicitationEnumOption `json:"oneOf,omitempty"`
	Items       *ElicitationArrayItems  `json:"items,omitempty"`
	Minimum     *float64                `json:"minimum,omitempty"`
	Maximum     *float64                `json:"maximum,omitempty"`
	MinLength   *uint32                 `json:"minLength,omitempty"`
	MaxLength   *uint32                 `json:"maxLength,omitempty"`
	MinItems    *uint64                 `json:"minItems,omitempty"`
	MaxItems    *uint64                 `json:"maxItems,omitempty"`
	Meta        map[string]any          `json:"_meta,omitempty"`
}
type ElicitationEnumOption struct {
	Const string `json:"const"`
	Title string `json:"title,omitempty"`
}
type ElicitationArrayItems struct {
	Type  string                  `json:"type,omitempty"`
	Enum  []string                `json:"enum,omitempty"`
	AnyOf []ElicitationEnumOption `json:"anyOf,omitempty"`
}
type ElicitationResponse struct {
	Action  string         `json:"action"`
	Content map[string]any `json:"content,omitempty"`
	Meta    map[string]any `json:"_meta,omitempty"`
}
type ElicitationCompletion struct {
	ElicitationID string             `json:"elicitationId"`
	Meta          map[string]any     `json:"_meta,omitempty"`
	Request       ElicitationRequest `json:"-"`
}
type pendingElicitation struct {
	lease    func() bool
	request  ElicitationRequest
	accepted bool
	early    *ElicitationCompletion
	timer    *time.Timer
}
type elicitationDelivery struct {
	event ElicitationCompletion
	lease func() bool
}
type elicitationState struct {
	completions      chan elicitationDelivery
	dropped          atomic.Uint64
	completionCtx    context.Context
	completionCancel context.CancelFunc
	mu               sync.Mutex
	pending          map[string]*pendingElicitation
	seen             map[string]struct{}
	handlers         ElicitationHandlers
}

func mergeElicitationHandlers(h, fallback ElicitationHandlers) ElicitationHandlers {
	if h.Form == nil {
		h.Form = fallback.Form
	}
	if h.URL == nil {
		h.URL = fallback.URL
	}
	if h.OnComplete == nil {
		h.OnComplete = fallback.OnComplete
	}
	if h.Timeout == 0 {
		h.Timeout = fallback.Timeout
	}
	return h
}
func elicitationCapabilities(h ElicitationHandlers) *ElicitationCapabilities {
	if h.Form == nil && h.URL == nil {
		return nil
	}
	caps := &ElicitationCapabilities{}
	if h.Form != nil {
		caps.Form = &EmptyCapability{}
	}
	if h.URL != nil {
		caps.URL = &EmptyCapability{}
	}
	return caps
}
func (c *Connection) registerElicitationHandlers(h ElicitationHandlers) {
	state := &elicitationState{pending: make(map[string]*pendingElicitation), seen: make(map[string]struct{}), handlers: h}
	c.elicitation = state
	state.completionCtx, state.completionCancel = context.WithCancel(context.Background())
	if h.URL != nil && h.OnComplete != nil {
		state.completions = make(chan elicitationDelivery, maxConcurrentAuthorityCalls)
		go func() {
			for {
				select {
				case <-state.completionCtx.Done():
					return
				case delivery := <-state.completions:
					if state.completionCtx.Err() == nil && delivery.lease() {
						c.invokeElicitationCompletion(state, delivery.event, delivery.lease)
					}
				}
			}
		}()
	}
	if h.URL != nil {
		go func() {
			<-c.peer.Done()
			state.completionCancel()
			state.mu.Lock()
			defer state.mu.Unlock()
			for id, p := range state.pending {
				if p.timer != nil {
					p.timer.Stop()
				}
				delete(state.pending, id)
			}
		}()
	}
	c.peer.RegisterRequest("elicitation/create", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var req ElicitationRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, invalidElicitation("invalid elicitation parameters")
		}
		if err := validateElicitationRequest(req); err != nil {
			return nil, invalidElicitation(err.Error())
		}
		var handler ElicitationHandler
		switch req.Mode {
		case "form":
			handler = h.Form
		case "url":
			handler = h.URL
		default:
			return nil, invalidElicitation("unsupported elicitation mode")
		}
		if handler == nil {
			return nil, invalidElicitation("elicitation mode was not advertised")
		}
		lease := c.elicitationLeaseFor(req)
		if len(req.RequestID) > 0 {
			scoped, stop := c.elicitationRequestContext(ctx, req.RequestID)
			defer stop()
			ctx = scoped
		}
		if !lease() {
			return ElicitationResponse{Action: "cancel"}, nil
		}
		if req.Mode == "url" {
			state.mu.Lock()
			if _, exists := state.seen[req.ElicitationID]; exists {
				state.mu.Unlock()
				return nil, invalidElicitation("elicitationId was already used on this connection")
			}
			if len(state.seen) >= 4096 {
				state.mu.Unlock()
				return nil, invalidElicitation("elicitation identifier budget exhausted; reconnect before continuing")
			}
			if len(state.pending) >= maxConcurrentAuthorityCalls {
				state.mu.Unlock()
				return nil, invalidElicitation("too many outstanding URL elicitations")
			}
			state.seen[req.ElicitationID] = struct{}{}
			state.pending[req.ElicitationID] = &pendingElicitation{request: req, lease: lease}
			state.mu.Unlock()
		}
		response, err := runAuthority(c, ctx, req.SessionID, h.Timeout, func(callCtx Context) (ElicitationResponse, error) { return handler(callCtx, cloneOwned(req)) })
		if err != nil || !lease() {
			response = ElicitationResponse{Action: "cancel"}
		}
		if err := validateElicitationResponse(req, response); err != nil {
			response = ElicitationResponse{Action: "cancel"}
		}
		if response.Action != "accept" || req.Mode == "url" {
			response.Content = nil
		}
		if req.Mode == "url" {
			state.mu.Lock()
			pending := state.pending[req.ElicitationID]
			var complete *ElicitationCompletion
			if pending != nil {
				if response.Action != "accept" {
					delete(state.pending, req.ElicitationID)
				} else {
					pending.accepted = true
					complete = pending.early
					if complete != nil {
						delete(state.pending, req.ElicitationID)
					} else {
						timeout := h.Timeout
						if timeout <= 0 {
							timeout = defaultAuthorityTimeout
						}
						pending.timer = time.AfterFunc(timeout, func() {
							state.mu.Lock()
							if state.pending[req.ElicitationID] == pending {
								delete(state.pending, req.ElicitationID)
							}
							state.mu.Unlock()
						})
					}
				}
			} else {
				response = ElicitationResponse{Action: "cancel"}
			}
			state.mu.Unlock()
			if complete != nil {
				state.deliverCompletion(*complete, lease)
			}
		}
		return response, nil
	})
	c.peer.RegisterNotification("elicitation/complete", func(ctx context.Context, raw json.RawMessage) {
		select {
		case <-c.peer.Done():
			return
		default:
		}
		var event ElicitationCompletion
		if json.Unmarshal(raw, &event) != nil || event.ElicitationID == "" {
			return
		}
		state.mu.Lock()
		pending := state.pending[event.ElicitationID]
		if pending == nil {
			state.mu.Unlock()
			return
		}
		event.Request = pending.request
		if !pending.accepted {
			pending.early = &event
			state.mu.Unlock()
			return
		}
		delete(state.pending, event.ElicitationID)
		if pending.timer != nil {
			pending.timer.Stop()
		}
		state.mu.Unlock()
		state.deliverCompletion(event, pending.lease)
	})
}
func invalidElicitation(message string) error { return &RPCError{Code: -32602, Message: message} }
func (s *elicitationState) cancelSession(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, p := range s.pending {
		if p.request.SessionID == sessionID {
			if p.timer != nil {
				p.timer.Stop()
			}
			delete(s.pending, id)
		}
	}
}
func (c *Connection) SetElicitationLease(lease func(ElicitationRequest) func() bool) {
	c.permissionObserverMu.Lock()
	defer c.permissionObserverMu.Unlock()
	c.elicitationLease = lease
}
func (c *Connection) elicitationLeaseFor(req ElicitationRequest) func() bool {
	c.permissionObserverMu.RLock()
	factory := c.elicitationLease
	c.permissionObserverMu.RUnlock()
	lease := func() bool { return true }
	if factory != nil {
		lease = factory(req)
		if lease == nil {
			return func() bool { return false }
		}
	}
	if len(req.RequestID) == 0 {
		return lease
	}
	lifetime, ok := c.peer.requestLifetime(req.RequestID)
	if !ok {
		return func() bool { return false }
	}
	return func() bool {
		select {
		case <-lifetime:
			return false
		default:
			return lease()
		}
	}
}
func (c *Connection) elicitationRequestContext(ctx context.Context, id json.RawMessage) (context.Context, context.CancelFunc) {
	scoped, cancel := context.WithCancel(ctx)
	lifetime, ok := c.peer.requestLifetime(id)
	if !ok {
		cancel()
		return scoped, cancel
	}
	go func() {
		select {
		case <-lifetime:
			cancel()
		case <-scoped.Done():
		}
	}()
	return scoped, cancel
}

func validateElicitationRequest(r ElicitationRequest) error {
	if r.Message == "" {
		return fmt.Errorf("elicitation message is required")
	}
	requestScoped := len(r.RequestID) > 0
	if (r.SessionID != "") == requestScoped || (r.ToolCallID != nil && r.SessionID == "") {
		return fmt.Errorf("elicitation requires exactly one session or request scope")
	}
	if requestScoped {
		var id any
		dec := json.NewDecoder(strings.NewReader(string(r.RequestID)))
		dec.UseNumber()
		if dec.Decode(&id) != nil {
			return fmt.Errorf("invalid requestId")
		}
		switch v := id.(type) {
		case nil, string:
		case json.Number:
			if _, err := v.Int64(); err != nil {
				return fmt.Errorf("requestId must be integer")
			}
		default:
			return fmt.Errorf("invalid requestId")
		}
	}
	switch r.Mode {
	case "form":
		if r.RequestedSchema == nil {
			return fmt.Errorf("form requires requestedSchema")
		}
		return validateElicitationSchema(*r.RequestedSchema)
	case "url":
		if r.ElicitationID == "" {
			return fmt.Errorf("URL elicitation requires elicitationId")
		}
		u, err := url.Parse(r.URL)
		if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
			return fmt.Errorf("URL elicitation requires an http(s) URL without credentials")
		}
		return nil
	default:
		return fmt.Errorf("unsupported elicitation mode")
	}
}
func validateElicitationSchema(s ElicitationSchema) error {
	if s.Type != "" && s.Type != "object" {
		return fmt.Errorf("elicitation schema must be object")
	}
	for _, name := range s.Required {
		if _, exists := s.Properties[name]; !exists {
			return fmt.Errorf("required field is not declared")
		}
	}
	for name, p := range s.Properties {
		sensitive := strings.ToLower(name + " " + p.Title + " " + p.Description + " " + p.Format)
		compact := strings.NewReplacer("_", "", "-", "", " ", "").Replace(sensitive)
		for _, secret := range []string{"password", "apikey", "accesstoken", "refreshtoken", "privatekey", "recoverycode", "creditcard", "paymentcredential", "secret"} {
			if strings.Contains(compact, secret) {
				return fmt.Errorf("credential collection is prohibited in form mode")
			}
		}
		switch p.Type {
		case "string":
		case "number", "integer", "boolean":
		case "array":
			if p.Items == nil || (p.Items.Type != "" && p.Items.Type != "string") {
				return fmt.Errorf("array field requires string items")
			}
		default:
			return fmt.Errorf("unsupported elicitation property type")
		}
		if p.Format != "" && p.Format != "email" && p.Format != "uri" && p.Format != "date" && p.Format != "date-time" {
			return fmt.Errorf("unsupported elicitation string format")
		}
		if p.Pattern != "" {
			if _, err := regexp.Compile(p.Pattern); err != nil {
				return fmt.Errorf("unsupported elicitation pattern")
			}
		}
	}
	return nil
}
func validateElicitationResponse(req ElicitationRequest, response ElicitationResponse) error {
	switch response.Action {
	case "decline", "cancel":
		return nil
	case "accept":
	default:
		return fmt.Errorf("unsupported elicitation action")
	}
	if req.Mode == "url" {
		return nil
	}
	s := req.RequestedSchema
	for _, name := range s.Required {
		if _, exists := response.Content[name]; !exists {
			return fmt.Errorf("required elicitation field missing")
		}
	}
	for name, v := range response.Content {
		p, exists := s.Properties[name]
		if !exists {
			return fmt.Errorf("undeclared elicitation field")
		}
		if err := validateElicitationValue(p, v); err != nil {
			return err
		}
	}
	return nil
}
func validateElicitationValue(p ElicitationProperty, v any) error {
	switch p.Type {
	case "string":
		x, ok := v.(string)
		if !ok {
			return fmt.Errorf("string value required")
		}
		n := uint32(utf8.RuneCountInString(x))
		if p.MinLength != nil && n < *p.MinLength || p.MaxLength != nil && n > *p.MaxLength {
			return fmt.Errorf("string length out of range")
		}
		if p.Pattern != "" {
			ok, err := regexp.MatchString(p.Pattern, x)
			if err != nil || !ok {
				return fmt.Errorf("string does not match pattern")
			}
		}
		switch p.Format {
		case "email":
			parsed, err := mail.ParseAddress(x)
			if err != nil || parsed.Address != x {
				return fmt.Errorf("invalid email address")
			}
		case "uri":
			parsed, err := url.Parse(x)
			if err != nil || parsed.Scheme == "" {
				return fmt.Errorf("invalid URI")
			}
		case "date":
			if _, err := time.Parse("2006-01-02", x); err != nil {
				return fmt.Errorf("invalid date")
			}
		case "date-time":
			if _, err := time.Parse(time.RFC3339, x); err != nil {
				return fmt.Errorf("invalid date-time")
			}
		}
		if p.Enum != nil && !stringMember(p.Enum, x) {
			return fmt.Errorf("invalid enum value")
		}
		if p.OneOf != nil && !enumMember(p.OneOf, x) {
			return fmt.Errorf("invalid enum value")
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("boolean value required")
		}
	case "number", "integer":
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Errorf("invalid numeric value")
		}
		var n float64
		if string(b) == "null" || json.Unmarshal(b, &n) != nil {
			return fmt.Errorf("numeric value required")
		}
		if math.IsNaN(n) || math.IsInf(n, 0) || p.Type == "integer" && math.Trunc(n) != n {
			return fmt.Errorf("invalid numeric value")
		}
		if p.Minimum != nil && n < *p.Minimum || p.Maximum != nil && n > *p.Maximum {
			return fmt.Errorf("number out of range")
		}
	case "array":
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		var x []string
		if string(b) == "null" || json.Unmarshal(b, &x) != nil {
			return fmt.Errorf("string array required")
		}
		if p.MinItems != nil && uint64(len(x)) < *p.MinItems || p.MaxItems != nil && uint64(len(x)) > *p.MaxItems {
			return fmt.Errorf("array length out of range")
		}
		for _, item := range x {
			if p.Items.Enum != nil && !stringMember(p.Items.Enum, item) {
				return fmt.Errorf("invalid array enum value")
			}
			if p.Items.AnyOf != nil && !enumMember(p.Items.AnyOf, item) {
				return fmt.Errorf("invalid array enum value")
			}
		}
	default:
		return fmt.Errorf("unsupported elicitation property")
	}
	return nil
}
func stringMember(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}
func enumMember(values []ElicitationEnumOption, value string) bool {
	for _, v := range values {
		if v.Const == value {
			return true
		}
	}
	return false
}

// ElicitationCompletionDrops reports completion callback deliveries discarded
// because the bounded host callback queue was full. Protocol completion state
// is still recorded; this diagnostic is scoped to this connection.
func (c *Connection) ElicitationCompletionDrops() uint64 {
	if c.elicitation == nil {
		return 0
	}
	return c.elicitation.dropped.Load()
}
func (s *elicitationState) deliverCompletion(event ElicitationCompletion, lease func() bool) {
	if s.completions == nil || s.completionCtx.Err() != nil || !lease() {
		return
	}
	select {
	case s.completions <- elicitationDelivery{event: cloneOwned(event), lease: lease}:
	default:
		s.dropped.Add(1)
	}
}

func (c *Connection) invokeElicitationCompletion(state *elicitationState, event ElicitationCompletion, lease func() bool) {
	timeout := state.handlers.Timeout
	if timeout <= 0 {
		timeout = defaultAuthorityTimeout
	}
	ctx, cancel := context.WithTimeout(state.completionCtx, timeout)
	c.stateMu.Lock()
	c.nextAuthorityID++
	id := c.nextAuthorityID
	c.authorityCancels[id] = authorityCancellation{sessionID: event.Request.SessionID, cancel: cancel}
	c.stateMu.Unlock()
	defer func() { cancel(); c.stateMu.Lock(); delete(c.authorityCancels, id); c.stateMu.Unlock() }()
	if ctx.Err() == nil && lease() {
		state.handlers.OnComplete(ctx, event)
	}
}
