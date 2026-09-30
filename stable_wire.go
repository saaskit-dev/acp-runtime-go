package acpruntime

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// EmptyCapability uses pointer presence, not map length, to advertise support.
type EmptyCapability struct {
	Meta map[string]any `json:"_meta,omitempty"`
}
type ClientSessionCapabilities struct {
	ConfigOptions *SessionConfigOptionsCapabilities `json:"configOptions,omitempty"`
}
type SessionConfigOptionsCapabilities struct {
	Boolean *EmptyCapability `json:"boolean,omitempty"`
}
type AuthCapabilities struct {
	Terminal bool `json:"terminal"`
}
type ElicitationCapabilities struct {
	Form *EmptyCapability `json:"form,omitempty"`
	URL  *EmptyCapability `json:"url,omitempty"`
}
type UsageCost struct {
	Amount   float64        `json:"amount"`
	Currency string         `json:"currency"`
	Meta     map[string]any `json:"_meta,omitempty"`
}

// Preserve the public map API while retaining advertised empty objects.
func (c SessionCapabilities) MarshalJSON() ([]byte, error) {
	fields := map[string]any{}
	for k, v := range map[string]map[string]any{"close": c.Close, "fork": c.Fork, "list": c.List, "resume": c.Resume, "delete": c.Delete, "additionalDirectories": c.AdditionalDirectories, "_meta": c.Meta} {
		if v != nil {
			fields[k] = v
		}
	}
	return json.Marshal(fields)
}

// load/resume have no sessionId in their stable response. The legacy field is
// read only to detect a conflicting response; identity remains request-owned.
type existingSessionWireResponse struct {
	SessionID         *string               `json:"sessionId,omitempty"`
	Modes             *SessionModeState     `json:"modes,omitempty"`
	Models            *SessionModelState    `json:"models,omitempty"`
	ConfigOptions     []SessionConfigOption `json:"configOptions,omitempty"`
	AvailableCommands []AvailableCommand    `json:"availableCommands,omitempty"`
	Meta              map[string]any        `json:"_meta,omitempty"`
}

func (r existingSessionWireResponse) normalized(id string) (NewSessionResponse, error) {
	if r.SessionID != nil && *r.SessionID != "" && *r.SessionID != id {
		return NewSessionResponse{}, fmt.Errorf("response sessionId conflicts with requested identity")
	}
	return NewSessionResponse{SessionID: id, Modes: r.Modes, Models: r.Models, ConfigOptions: r.ConfigOptions, AvailableCommands: r.AvailableCommands, Meta: r.Meta}, nil
}

// Independent protocol DTO: host approval types are deliberately not used as
// the wire request structure. Unknown fields are retained for approval context.
type permissionWireRequest struct {
	SessionID string                  `json:"sessionId"`
	ToolCall  *permissionWireToolCall `json:"toolCall"`
	Options   []permissionWireOption  `json:"options"`
	Meta      map[string]any          `json:"_meta,omitempty"`
}
type permissionWireToolCall struct {
	ToolCallID string          `json:"toolCallId"`
	Title      *string         `json:"title,omitempty"`
	Kind       *string         `json:"kind,omitempty"`
	Name       *string         `json:"name,omitempty"`
	Status     *string         `json:"status,omitempty"`
	RawInput   json.RawMessage `json:"rawInput,omitempty"`
	RawOutput  json.RawMessage `json:"rawOutput,omitempty"`
	Content    ContentBlocks   `json:"content,omitempty"`
	Locations  []ToolLocation  `json:"locations,omitempty"`
	Meta       map[string]any  `json:"_meta,omitempty"`
}
type permissionWireOption struct {
	ID   string         `json:"optionId"`
	Name string         `json:"name"`
	Kind string         `json:"kind"`
	Meta map[string]any `json:"_meta,omitempty"`
}
type permissionWireOutcome struct {
	Outcome  string `json:"outcome"`
	OptionID string `json:"optionId,omitempty"`
}
type permissionResponse struct {
	Outcome permissionWireOutcome `json:"outcome"`
}

func (r *PermissionRequest) UnmarshalJSON(data []byte) error {
	var wire permissionWireRequest
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	if wire.SessionID == "" || wire.ToolCall == nil || wire.ToolCall.ToolCallID == "" || wire.Options == nil {
		return fmt.Errorf("permission requires sessionId, toolCall.toolCallId and options")
	}
	out := PermissionRequest{SessionID: wire.SessionID, ToolCallID: wire.ToolCall.ToolCallID, Name: wire.ToolCall.Name, Status: wire.ToolCall.Status, RawInput: wire.ToolCall.RawInput, RawOutput: wire.ToolCall.RawOutput, Content: wire.ToolCall.Content, Locations: wire.ToolCall.Locations, Meta: wire.Meta, ToolCallMeta: wire.ToolCall.Meta}
	if wire.ToolCall.Title != nil {
		out.Title = *wire.ToolCall.Title
	}
	if wire.ToolCall.Kind != nil {
		out.Kind = *wire.ToolCall.Kind
	}
	seen := map[string]bool{}
	for _, o := range wire.Options {
		if o.ID == "" || o.Name == "" || o.Kind == "" || seen[o.ID] {
			return fmt.Errorf("permission option requires unique optionId, name, and kind")
		}
		seen[o.ID] = true
		out.Options = append(out.Options, PermissionOption{ID: o.ID, Name: o.Name, Kind: o.Kind, Meta: o.Meta})
	}
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(data, &raw)
	var toolRaw map[string]json.RawMessage
	_ = json.Unmarshal(raw["toolCall"], &toolRaw)
	for _, key := range []string{"sessionId", "toolCall", "options", "_meta"} {
		delete(raw, key)
	}
	for _, key := range []string{"toolCallId", "title", "kind", "name", "status", "rawInput", "rawOutput", "content", "locations", "_meta"} {
		delete(toolRaw, key)
	}
	out.Extra = raw
	out.ToolCallExtra = toolRaw
	*r = out
	return nil
}
func (r PermissionRequest) MarshalJSON() ([]byte, error) {
	tool := permissionWireToolCall{ToolCallID: r.ToolCallID, Name: r.Name, Status: r.Status, RawInput: r.RawInput, RawOutput: r.RawOutput, Content: r.Content, Locations: r.Locations, Meta: r.ToolCallMeta}
	if r.Title != "" {
		tool.Title = &r.Title
	}
	if r.Kind != "" {
		tool.Kind = &r.Kind
	}
	options := make([]permissionWireOption, 0, len(r.Options))
	for _, o := range r.Options {
		options = append(options, permissionWireOption{ID: o.ID, Name: o.Name, Kind: o.Kind, Meta: o.Meta})
	}
	b, err := json.Marshal(permissionWireRequest{SessionID: r.SessionID, ToolCall: &tool, Options: options, Meta: r.Meta})
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(b, &fields)
	var toolFields map[string]json.RawMessage
	_ = json.Unmarshal(fields["toolCall"], &toolFields)
	for k, v := range r.ToolCallExtra {
		if _, ok := toolFields[k]; !ok {
			toolFields[k] = v
		}
	}
	fields["toolCall"], err = json.Marshal(toolFields)
	if err != nil {
		return nil, err
	}
	for k, v := range r.Extra {
		if _, ok := fields[k]; !ok {
			fields[k] = v
		}
	}
	return json.Marshal(fields)
}
func (d PermissionDecision) MarshalJSON() ([]byte, error) {
	if d.Outcome != "cancelled" && (d.Outcome != "selected" || d.OptionID == "") {
		return nil, fmt.Errorf("invalid permission outcome")
	}
	if d.Outcome == "cancelled" {
		d.OptionID = ""
	}
	return json.Marshal(permissionResponse{Outcome: permissionWireOutcome{Outcome: d.Outcome, OptionID: d.OptionID}})
}
func (d *PermissionDecision) UnmarshalJSON(data []byte) error {
	var wire permissionResponse
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	if wire.Outcome.Outcome != "cancelled" && (wire.Outcome.Outcome != "selected" || wire.Outcome.OptionID == "") {
		return fmt.Errorf("invalid permission outcome")
	}
	*d = PermissionDecision{Outcome: wire.Outcome.Outcome, OptionID: wire.Outcome.OptionID}
	if d.Outcome == "cancelled" {
		d.OptionID = ""
	}
	return nil
}
func validatedPermissionDecision(req PermissionRequest, d PermissionDecision) PermissionDecision {
	if d.Outcome == "cancelled" {
		return PermissionDecision{Outcome: "cancelled"}
	}
	if d.Outcome == "selected" {
		for _, option := range req.Options {
			if option.ID == d.OptionID {
				switch option.Kind {
				case "allow_once", "allow_always", "reject_once", "reject_always":
					return d
				}
			}
		}
	}
	return PermissionDecision{Outcome: "cancelled"}
}

func (r SetSessionConfigOptionRequest) MarshalJSON() ([]byte, error) {
	type wire SetSessionConfigOptionRequest
	switch r.Value.(type) {
	case bool:
		if r.Type != "" && r.Type != "boolean" {
			return nil, fmt.Errorf("boolean config value requires type boolean")
		}
		r.Type = "boolean"
	case string:
		if r.Type == "boolean" {
			return nil, fmt.Errorf("boolean config value must be bool")
		}
	default:
		return nil, fmt.Errorf("config value must be string or boolean")
	}
	return json.Marshal(wire(r))
}
func (r *SetSessionConfigOptionRequest) UnmarshalJSON(data []byte) error {
	type wire SetSessionConfigOptionRequest
	var out wire
	if err := json.Unmarshal(data, &out); err != nil {
		return err
	}
	if out.Type == "boolean" {
		if _, ok := out.Value.(bool); !ok {
			return fmt.Errorf("boolean config value must be present bool")
		}
	} else if _, ok := out.Value.(string); !ok {
		return fmt.Errorf("select config value must be string")
	}
	if out.SessionID == "" || out.OptionID == "" {
		return fmt.Errorf("config request requires sessionId and configId")
	}
	*r = SetSessionConfigOptionRequest(out)
	return nil
}
func (o *SessionConfigOption) UnmarshalJSON(data []byte) error {
	type wire SessionConfigOption
	var out wire
	holder := struct {
		*wire
		Options json.RawMessage `json:"options"`
	}{wire: &out}
	if err := json.Unmarshal(data, &holder); err != nil {
		return err
	}
	if out.Type == "boolean" {
		if _, ok := out.Value.(bool); !ok {
			return fmt.Errorf("boolean currentValue must be present bool")
		}
	}
	if nonNullRaw(holder.Options) {
		var items []json.RawMessage
		if err := json.Unmarshal(holder.Options, &items); err != nil {
			return err
		}
		grouped := false
		for i, item := range items {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(item, &fields); err != nil {
				return err
			}
			_, isGroup := fields["group"]
			if i == 0 {
				grouped = isGroup
			} else if grouped != isGroup {
				return fmt.Errorf("config options cannot mix groups and choices")
			}
		}
		if grouped {
			if err := json.Unmarshal(holder.Options, &out.Groups); err != nil {
				return err
			}
			out.Options = nil
		} else {
			if err := json.Unmarshal(holder.Options, &out.Options); err != nil {
				return err
			}
		}
	}
	*o = SessionConfigOption(out)
	return nil
}
func (o SessionConfigOption) MarshalJSON() ([]byte, error) {
	type wire SessionConfigOption
	if o.Type == "boolean" {
		if _, ok := o.Value.(bool); !ok {
			return nil, fmt.Errorf("boolean currentValue must be bool")
		}
	}
	encoded, err := json.Marshal(wire(o))
	if err != nil {
		return nil, err
	}
	if o.Type != "select" {
		return encoded, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		return nil, err
	}
	delete(fields, "groups")
	if o.Groups != nil {
		fields["options"], err = json.Marshal(o.Groups)
	} else {
		options := o.Options
		if options == nil {
			options = []SessionConfigChoice{}
		}
		fields["options"], err = json.Marshal(options)
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(fields)
}
func (u *SessionUpdate) UnmarshalJSON(data []byte) error {
	type wire SessionUpdate
	var out wire
	if err := json.Unmarshal(data, &out); err != nil {
		return err
	}
	if out.SessionUpdate == "usage_update" && (out.Used == nil || out.Size == nil) {
		return fmt.Errorf("usage_update requires top-level used and size")
	}
	*u = SessionUpdate(out)
	return nil
}
func nonNullRaw(b json.RawMessage) bool {
	return len(b) > 0 && !bytes.Equal(bytes.TrimSpace(b), []byte("null"))
}

// ContextUsage is the stable cumulative context-window update, independent of
// experimental per-turn token counters.
type ContextUsage struct {
	Used uint64
	Size uint64
	Cost *UsageCost
}

func (c *Connection) requireCapability(method string) error {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	// Low-level connections may be used before initialize by test/factory code.
	// A completed handshake makes advertised capabilities authoritative.
	if !c.initialized {
		return nil
	}
	caps := c.agentCapabilities
	allowed := false
	switch method {
	case "session/load":
		allowed = caps.LoadSession
	case "session/resume":
		allowed = caps.SessionCapabilities.Resume != nil
	case "session/list":
		allowed = caps.SessionCapabilities.List != nil
	case "session/close":
		allowed = caps.SessionCapabilities.Close != nil
	case "session/delete":
		allowed = caps.SessionCapabilities.Delete != nil
	case "logout":
		v, ok := caps.Auth["logout"]
		_, object := v.(map[string]any)
		allowed = ok && v != nil && object
	}
	if !allowed {
		return fmt.Errorf("%s was not advertised by the agent", method)
	}
	return nil
}
func (c *Connection) rememberConfigOptions(sessionID string, options []SessionConfigOption) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	known := make(map[string]string, len(options))
	for _, option := range options {
		known[option.ID] = option.Type
	}
	c.configTypes[sessionID] = known
}

func (r NewSessionRequest) MarshalJSON() ([]byte, error) {
	type wire NewSessionRequest
	if r.MCPServers == nil {
		r.MCPServers = []MCPServer{}
	}
	return json.Marshal(wire(r))
}
func (r LoadSessionRequest) MarshalJSON() ([]byte, error) {
	type wire LoadSessionRequest
	if r.MCPServers == nil {
		r.MCPServers = []MCPServer{}
	}
	return json.Marshal(wire(r))
}
func (r ResumeSessionRequest) MarshalJSON() ([]byte, error) {
	type wire ResumeSessionRequest
	if r.MCPServers == nil {
		r.MCPServers = []MCPServer{}
	}
	return json.Marshal(wire(r))
}

// Preserve the public Text/Content convenience fields while always emitting a
// standard single ContentBlock for chunk notifications on the ACP wire.
func (u SessionUpdate) MarshalJSON() ([]byte, error) {
	type wire SessionUpdate
	if u.SessionUpdate == "usage_update" && (u.Used == nil || u.Size == nil) {
		return nil, fmt.Errorf("usage_update requires top-level used and size")
	}
	b, err := json.Marshal(wire(u))
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		return nil, err
	}
	switch u.SessionUpdate {
	case "agent_message_chunk", "agent_thought_chunk", "user_message_chunk":
		var content ContentBlock
		if len(u.Content) > 1 {
			return nil, fmt.Errorf("ACP content chunk requires exactly one content block")
		}
		if len(u.Content) == 1 {
			content = u.Content[0]
		} else {
			content = ContentBlock{Type: "text", Text: u.Text}
		}
		fields["content"], err = json.Marshal(content)
		delete(fields, "type")
		delete(fields, "text")
	case "plan":
		if u.Entries == nil {
			fields["entries"] = json.RawMessage("[]")
		}
	case "config_option_update":
		if u.ConfigOptions == nil {
			fields["configOptions"] = json.RawMessage("[]")
		}
	case "available_commands_update":
		if u.AvailableCommands == nil {
			fields["availableCommands"] = json.RawMessage("[]")
		}
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(fields)
}
func (c ContentBlock) MarshalJSON() ([]byte, error) {
	type wire ContentBlock
	b, err := json.Marshal(wire(c))
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		return nil, err
	}
	switch c.Type {
	case "text":
		fields["text"], err = json.Marshal(c.Text)
	case "diff":
		fields["newText"], err = json.Marshal(c.NewText)
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(fields)
}
