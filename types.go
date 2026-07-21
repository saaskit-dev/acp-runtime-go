package acpruntime

import (
	"encoding/json"
	"time"
)

const (
	ProtocolVersion = 1

	ACPProtocolSourceRepo        = "https://github.com/agentclientprotocol/agent-client-protocol"
	ACPProtocolSourceRef         = "schema-v1.17.0"
	ACPProtocolDocsURL           = "https://agentclientprotocol.com/protocol/v1/overview"
	ACPProtocolDocsSchemaURL     = "https://agentclientprotocol.com/protocol/v1/schema"
	ACPProtocolAlignmentVerified = "2026-07-05"

	RuntimeSnapshotVersion = 1

	RuntimeAuthenticationDefaultMethodMetaKey = "acp-runtime/default-auth-method"
	RuntimeTerminalAuthSuccessPatternsMetaKey = "acp-runtime/terminal-success-patterns"
)

type Agent struct {
	Type    string            `json:"type"`
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	// ExtraArgs are appended after Args without replacing the default launch
	// sequence. This is the safe way to add CLI flags (e.g. --disallowedTools)
	// to an agent built by CreateClaudeCodeAgent/CreateCodexAgent: setting Args
	// directly would overwrite the "npm exec ... --" preamble and break spawn.
	// mergeAgent folds ExtraArgs into Args, so stdio spawns the combined slice.
	ExtraArgs []string          `json:"extraArgs,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
}

type Implementation struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type EnvVariable struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type HTTPHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type MCPServer struct {
	Name    string            `json:"name"`
	Type    string            `json:"type,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers []HTTPHeader      `json:"headers,omitempty"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	CWD     string            `json:"cwd,omitempty"`
	Env     []EnvVariable     `json:"env,omitempty"`
	Meta    map[string]any    `json:"_meta,omitempty"`
	Raw     json.RawMessage   `json:"-"`
	Extra   map[string]string `json:"-"`
}

func (s MCPServer) MarshalJSON() ([]byte, error) {
	type stdioWire struct {
		Name    string         `json:"name"`
		Command string         `json:"command,omitempty"`
		Args    []string       `json:"args"`
		CWD     string         `json:"cwd,omitempty"`
		Env     []EnvVariable  `json:"env"`
		Meta    map[string]any `json:"_meta,omitempty"`
	}
	type httpWire struct {
		Name    string         `json:"name"`
		Type    string         `json:"type,omitempty"`
		URL     string         `json:"url,omitempty"`
		Headers []HTTPHeader   `json:"headers"`
		Meta    map[string]any `json:"_meta,omitempty"`
	}
	if s.isRemoteMCPServer() {
		headers := s.Headers
		if headers == nil {
			headers = []HTTPHeader{}
		}
		return json.Marshal(httpWire{
			Name:    s.Name,
			Type:    s.Type,
			URL:     s.URL,
			Headers: headers,
			Meta:    s.Meta,
		})
	}
	args := s.Args
	if args == nil {
		args = []string{}
	}
	env := s.Env
	if env == nil {
		env = []EnvVariable{}
	}
	return json.Marshal(stdioWire{
		Name:    s.Name,
		Command: s.Command,
		Args:    args,
		CWD:     s.CWD,
		Env:     env,
		Meta:    s.Meta,
	})
}

func (s *MCPServer) UnmarshalJSON(data []byte) error {
	type wire MCPServer
	var out wire
	if err := json.Unmarshal(data, &out); err != nil {
		return err
	}
	*s = MCPServer(out)
	if len(data) > 0 {
		s.Raw = append(s.Raw[:0], data...)
	}
	if s.isRemoteMCPServer() {
		if s.Headers == nil {
			s.Headers = []HTTPHeader{}
		}
	} else {
		if s.Args == nil {
			s.Args = []string{}
		}
		if s.Env == nil {
			s.Env = []EnvVariable{}
		}
	}
	return nil
}

func (s MCPServer) isRemoteMCPServer() bool {
	return s.Type == "http" || s.Type == "sse" || s.URL != ""
}

type ClientCapabilities struct {
	Terminal bool                       `json:"terminal"`
	FS       FilesystemCapabilities     `json:"fs"`
	Meta     map[string]json.RawMessage `json:"_meta,omitempty"`
}

type FilesystemCapabilities struct {
	ReadTextFile  bool `json:"readTextFile"`
	WriteTextFile bool `json:"writeTextFile"`
}

type AgentCapabilities struct {
	LoadSession         bool                       `json:"loadSession,omitempty"`
	Auth                map[string]any             `json:"auth,omitempty"`
	PromptCapabilities  PromptCapabilities         `json:"promptCapabilities,omitempty"`
	MCPCapabilities     MCPCapabilities            `json:"mcpCapabilities,omitempty"`
	SessionCapabilities SessionCapabilities        `json:"sessionCapabilities,omitempty"`
	Meta                map[string]json.RawMessage `json:"_meta,omitempty"`
	Raw                 map[string]json.RawMessage `json:"-"`
}

type PromptCapabilities struct {
	Audio           bool `json:"audio"`
	Image           bool `json:"image"`
	EmbeddedContext bool `json:"embeddedContext"`
}

type MCPCapabilities struct {
	HTTP bool `json:"http"`
	SSE  bool `json:"sse"`
}

type SessionCapabilities struct {
	Close                 map[string]any `json:"close,omitempty"`
	Fork                  map[string]any `json:"fork,omitempty"`
	List                  map[string]any `json:"list,omitempty"`
	Resume                map[string]any `json:"resume,omitempty"`
	AdditionalDirectories map[string]any `json:"additionalDirectories,omitempty"`
}

type InitializeRequest struct {
	ProtocolVersion    int                `json:"protocolVersion"`
	ClientInfo         *Implementation    `json:"clientInfo,omitempty"`
	ClientCapabilities ClientCapabilities `json:"clientCapabilities,omitempty"`
	Meta               map[string]any     `json:"_meta,omitempty"`
}

type InitializeResponse struct {
	ProtocolVersion   int               `json:"protocolVersion"`
	AgentInfo         *Implementation   `json:"agentInfo,omitempty"`
	AgentCapabilities AgentCapabilities `json:"agentCapabilities,omitempty"`
	AuthMethods       []AuthMethod      `json:"authMethods,omitempty"`
	Meta              map[string]any    `json:"_meta,omitempty"`
}

type AuthMethod struct {
	Type        string            `json:"type,omitempty"`
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description *string           `json:"description,omitempty"`
	Link        *string           `json:"link,omitempty"`
	Vars        []AuthEnvVar      `json:"vars,omitempty"`
	Args        []string          `json:"args,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	Meta        map[string]any    `json:"_meta,omitempty"`
}

type AuthEnvVar struct {
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
	Required    bool    `json:"required,omitempty"`
}

type AuthenticateRequest struct {
	MethodID string         `json:"methodId"`
	Meta     map[string]any `json:"_meta,omitempty"`
}

type AuthenticateResponse struct {
	Meta map[string]any `json:"_meta,omitempty"`
}

type NewSessionRequest struct {
	CWD                   string         `json:"cwd"`
	MCPServers            []MCPServer    `json:"mcpServers"`
	AdditionalDirectories []string       `json:"additionalDirectories,omitempty"`
	Meta                  map[string]any `json:"_meta,omitempty"`
}

type NewSessionResponse struct {
	SessionID         string                `json:"sessionId"`
	Modes             *SessionModeState     `json:"modes,omitempty"`
	Models            *SessionModelState    `json:"models,omitempty"`
	ConfigOptions     []SessionConfigOption `json:"configOptions,omitempty"`
	AvailableCommands []AvailableCommand    `json:"availableCommands,omitempty"`
	Meta              map[string]any        `json:"_meta,omitempty"`
}

type LoadSessionRequest struct {
	SessionID             string      `json:"sessionId"`
	CWD                   string      `json:"cwd"`
	MCPServers            []MCPServer `json:"mcpServers"`
	AdditionalDirectories []string    `json:"additionalDirectories,omitempty"`
}

type LoadSessionResponse = NewSessionResponse

type ResumeSessionRequest struct {
	SessionID             string         `json:"sessionId"`
	CWD                   string         `json:"cwd"`
	MCPServers            []MCPServer    `json:"mcpServers"`
	AdditionalDirectories []string       `json:"additionalDirectories,omitempty"`
	Meta                  map[string]any `json:"_meta,omitempty"`
}

type ResumeSessionResponse = NewSessionResponse

type ForkSessionRequest struct {
	SessionID             string      `json:"sessionId"`
	CWD                   string      `json:"cwd"`
	MCPServers            []MCPServer `json:"mcpServers"`
	AdditionalDirectories []string    `json:"additionalDirectories,omitempty"`
}

type ForkSessionResponse = NewSessionResponse

type ListSessionsRequest struct {
	CWD                   string   `json:"cwd"`
	Cursor                *string  `json:"cursor,omitempty"`
	AdditionalDirectories []string `json:"additionalDirectories,omitempty"`
}

type SessionInfo struct {
	SessionID             string   `json:"sessionId"`
	CWD                   string   `json:"cwd"`
	Title                 *string  `json:"title,omitempty"`
	UpdatedAt             *string  `json:"updatedAt,omitempty"`
	AdditionalDirectories []string `json:"additionalDirectories,omitempty"`
}

type ListSessionsResponse struct {
	Sessions   []SessionInfo `json:"sessions"`
	NextCursor *string       `json:"nextCursor,omitempty"`
}

type CloseSessionRequest struct {
	SessionID string `json:"sessionId"`
}

type CloseSessionResponse struct{}

// DeleteSessionRequest deletes a session's persistent history. Unlike
// session/close (which just ends the active session), session/delete removes
// the session from the agent's storage so it can no longer be loaded/resumed.
type DeleteSessionRequest struct {
	SessionID string `json:"sessionId"`
}

type DeleteSessionResponse struct{}

// LogoutRequest asks the agent to discard any cached credentials. Stable since
// ACP v1; agents that advertised auth methods during initialize should honor it.
type LogoutRequest struct{}

type LogoutResponse struct{}

type CancelRequest struct {
	SessionID string `json:"sessionId"`
}

type SetSessionModeRequest struct {
	SessionID string `json:"sessionId"`
	ModeID    string `json:"modeId"`
}

type SetSessionModeResponse struct{}

type SetSessionConfigOptionRequest struct {
	SessionID string         `json:"sessionId"`
	OptionID  string         `json:"configId"`
	Value     any            `json:"value"`
	Meta      map[string]any `json:"_meta,omitempty"`
}

func (r *SetSessionConfigOptionRequest) UnmarshalJSON(data []byte) error {
	type wire struct {
		SessionID string         `json:"sessionId"`
		ConfigID  string         `json:"configId"`
		OptionID  string         `json:"optionId"`
		Value     any            `json:"value"`
		Meta      map[string]any `json:"_meta,omitempty"`
	}
	var out wire
	if err := json.Unmarshal(data, &out); err != nil {
		return err
	}
	r.SessionID = out.SessionID
	r.OptionID = out.ConfigID
	if r.OptionID == "" {
		r.OptionID = out.OptionID
	}
	r.Value = out.Value
	r.Meta = out.Meta
	return nil
}

type SetSessionConfigOptionResponse struct {
	// ConfigOptions is a pointer so clients can distinguish an older provider
	// that omits the field from a provider that explicitly returns an empty
	// full snapshot.
	ConfigOptions *[]SessionConfigOption `json:"configOptions,omitempty"`
}

type PromptRequest struct {
	SessionID string         `json:"sessionId"`
	Prompt    []ContentBlock `json:"prompt"`
	MessageID *string        `json:"messageId,omitempty"`
	Meta      map[string]any `json:"_meta,omitempty"`
}

type PromptResponse struct {
	StopReason    string         `json:"stopReason"`
	Usage         *Usage         `json:"usage,omitempty"`
	UserMessageID *string        `json:"userMessageId,omitempty"`
	Meta          map[string]any `json:"_meta,omitempty"`
}

type ContentBlock struct {
	Type        string          `json:"type"`
	Text        string          `json:"text,omitempty"`
	MimeType    string          `json:"mimeType,omitempty"`
	Data        string          `json:"data,omitempty"`
	URI         string          `json:"uri,omitempty"`
	Name        string          `json:"name,omitempty"`
	Resource    json.RawMessage `json:"resource,omitempty"`
	Annotations json.RawMessage `json:"annotations,omitempty"`
	Meta        map[string]any  `json:"_meta,omitempty"`
}

type ContentBlocks []ContentBlock

func (blocks *ContentBlocks) UnmarshalJSON(data []byte) error {
	var list []ContentBlock
	if err := json.Unmarshal(data, &list); err == nil {
		*blocks = list
		return nil
	}
	var single ContentBlock
	if err := json.Unmarshal(data, &single); err != nil {
		return err
	}
	*blocks = []ContentBlock{single}
	return nil
}

type SessionMode struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Meta        map[string]any `json:"_meta,omitempty"`
}

type SessionModeState struct {
	CurrentModeID  string         `json:"currentModeId"`
	AvailableModes []SessionMode  `json:"availableModes"`
	Meta           map[string]any `json:"_meta,omitempty"`
}

type ModelInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type SessionModelState struct {
	CurrentModelID  string      `json:"currentModelId,omitempty"`
	AvailableModels []ModelInfo `json:"availableModels,omitempty"`
}

type SessionConfigOption struct {
	Type        string                `json:"type"`
	ID          string                `json:"id"`
	Name        string                `json:"name"`
	Description *string               `json:"description,omitempty"`
	Category    *string               `json:"category,omitempty"`
	Value       any                   `json:"currentValue"`
	Options     []SessionConfigChoice `json:"options,omitempty"`
	Groups      []SessionConfigGroup  `json:"groups,omitempty"`
	Meta        map[string]any        `json:"_meta,omitempty"`
}

type SessionConfigChoice struct {
	Value       any    `json:"value"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type SessionConfigGroup struct {
	ID      string                `json:"id"`
	Name    string                `json:"name"`
	Options []SessionConfigChoice `json:"options"`
}

type SessionNotification struct {
	SessionID string        `json:"sessionId"`
	Update    SessionUpdate `json:"update"`
}

type SessionUpdate struct {
	SessionUpdate     string                `json:"sessionUpdate"`
	Type              string                `json:"type,omitempty"`
	Text              string                `json:"text,omitempty"`
	ToolCallID        string                `json:"toolCallId,omitempty"`
	Title             *string               `json:"title,omitempty"`
	Kind              *string               `json:"kind,omitempty"`
	Status            *string               `json:"status,omitempty"`
	Locations         []ToolLocation        `json:"locations,omitempty"`
	Content           ContentBlocks         `json:"content,omitempty"`
	RawInput          json.RawMessage       `json:"rawInput,omitempty"`
	RawOutput         json.RawMessage       `json:"rawOutput,omitempty"`
	Entries           []PlanEntry           `json:"entries,omitempty"`
	CurrentModeID     string                `json:"currentModeId,omitempty"`
	AvailableModes    []SessionMode         `json:"availableModes,omitempty"`
	ConfigOptions     []SessionConfigOption `json:"configOptions,omitempty"`
	AvailableCommands []AvailableCommand    `json:"availableCommands,omitempty"`
	SessionInfoUpdate
	Usage *Usage         `json:"usage,omitempty"`
	Meta  map[string]any `json:"_meta,omitempty"`
}

type PlanEntry struct {
	Content  string `json:"content"`
	Status   string `json:"status"`
	Priority string `json:"priority,omitempty"`
}

type ToolLocation struct {
	Path   string `json:"path,omitempty"`
	Line   *int   `json:"line,omitempty"`
	Column *int   `json:"column,omitempty"`
}

type SessionInfoUpdate struct {
	Title     *string `json:"title,omitempty"`
	UpdatedAt *string `json:"updatedAt,omitempty"`
}

type Usage struct {
	TotalTokens       uint64  `json:"totalTokens"`
	InputTokens       uint64  `json:"inputTokens"`
	OutputTokens      uint64  `json:"outputTokens"`
	ThoughtTokens     *uint64 `json:"thoughtTokens,omitempty"`
	CachedReadTokens  *uint64 `json:"cachedReadTokens,omitempty"`
	CachedWriteTokens *uint64 `json:"cachedWriteTokens,omitempty"`
}

type RuntimeOptions struct {
	ClientInfo            Implementation
	HomeDir               string
	CacheDir              string
	StoredSessionsEnabled bool
	AuthenticationHandler AuthenticationHandler
	AuthorityHandlers     AuthorityHandlers
	Observability         ObservabilityOptions
	// Hooks is an optional lightweight observability surface for hosts that want
	// session/turn/process lifecycle signals without pulling in a full metrics
	// stack. Nil fields are ignored.
	Hooks RuntimeHooks
	// ReadModelLimits bounds in-memory session history. Zero fields use defaults.
	ReadModelLimits ReadModelLimits
}

// RuntimeHooks are best-effort callbacks for operational telemetry. Implementations
// must be non-blocking; the runtime may invoke them on RPC or process paths.
type RuntimeHooks struct {
	OnSessionEvent func(RuntimeSessionEvent)
	OnTurnEvent    func(RuntimeTurnEvent)
	OnProcessEvent func(RuntimeProcessEvent)
	OnEventDrop    func(RuntimeEventDrop)
}

type RuntimeSessionEvent struct {
	Type      string // created | resumed | loaded | forked | closed | deleted | cleanup_failed
	SessionID string
	AgentType string
	Err       error
}

type RuntimeTurnEvent struct {
	Type      string // started | completed | failed | cancelled | coalesced
	SessionID string
	TurnID    string
	Duration  time.Duration
	Err       error
}

type RuntimeProcessEvent struct {
	Type    string // spawn | teardown | force_kill | wait_timeout
	Command string
	Err     error
}

type RuntimeEventDrop struct {
	SessionID string
	TurnID    string
	EventType string
}

type AuthenticationHandler func(ctx Context, methods []RuntimeAuthenticationMethod) (RuntimeAuthenticationDecision, error)

type Context interface {
	Done() <-chan struct{}
	Err() error
}

type RuntimeAuthenticationMethod struct {
	Type        string
	ID          string
	Name        string
	Description string
	Link        string
	Vars        []AuthEnvVar
	Args        []string
	Env         map[string]string
	Meta        map[string]any
}

type RuntimeAuthenticationDecision struct {
	MethodID string
}

type AuthorityHandlers struct {
	Permission PermissionHandler
	Filesystem FilesystemHandler
	Terminal   TerminalHandler
}

type PermissionHandler func(ctx Context, request PermissionRequest) (PermissionDecision, error)

type PermissionRequest struct {
	SessionID  string
	ToolCallID string
	Title      string
	Kind       string
	Target     string
	Options    []PermissionOption
}

type PermissionOption struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	Kind string `json:"kind,omitempty"`
}

type PermissionDecision struct {
	Outcome  string
	OptionID string
}

type FilesystemHandler interface {
	ReadTextFile(ctx Context, path string) (string, error)
	WriteTextFile(ctx Context, path, text string) error
}

// TerminalHandler implements the host side of the ACP terminal methods
// (terminal/create, terminal/output, terminal/wait_for_exit, terminal/kill,
// terminal/release). All five are invoked by the agent and implemented by the
// host; see https://agentclientprotocol.com/protocol/v1/terminals.
type TerminalHandler interface {
	CreateTerminal(ctx Context, request CreateTerminalRequest) (CreateTerminalResult, error)
	Output(ctx Context, terminalID string) (TerminalOutputResult, error)
	WaitForExit(ctx Context, terminalID string) (TerminalExitStatus, error)
	Kill(ctx Context, terminalID string) error
	Release(ctx Context, terminalID string) error
}

// CreateTerminalRequest is the runtime representation of a terminal/create
// request. Env is []EnvVariable on the wire (ACP v1), not a map.
type CreateTerminalRequest struct {
	SessionID       string
	Command         string
	Args            []string
	Env             []EnvVariable
	CWD             string
	OutputByteLimit *uint64
}

// CreateTerminalResult is returned immediately by CreateTerminal; the command
// keeps running asynchronously and is observed via Output/WaitForExit.
type CreateTerminalResult struct {
	TerminalID string
}

// TerminalOutputResult is the runtime representation of terminal/output. It
// returns the output captured so far without blocking for exit.
type TerminalOutputResult struct {
	Output     string
	Truncated  bool
	ExitStatus *TerminalExitStatus // present only after the command has exited
}

// TerminalExitStatus conveys how a terminal command ended. Normal exit sets
// ExitCode (and leaves Signal nil); termination by signal sets Signal (and
// leaves ExitCode nil). This mirrors the ACP TerminalExitStatus shape.
type TerminalExitStatus struct {
	ExitCode *uint32 // nil when terminated by signal
	Signal   *string // nil when exited normally
}

type ObservabilityOptions struct {
	CaptureContent  string
	OnProtocolError ProtocolErrorHandler
}

// Default read-model caps keep long-lived sessions from retaining unbounded
// history. Zero means "use default"; negative means unlimited.
const (
	DefaultMaxThreadEntries      = 256
	DefaultMaxToolCallEntries    = 128
	DefaultMaxPermissionEntries  = 64
)

// ReadModelLimits bounds in-memory session history retained for Snapshot and
// query APIs. Completed tool/permission entries are dropped first; thread is a
// FIFO ring of the most recent messages.
type ReadModelLimits struct {
	MaxThreadEntries     int
	MaxToolCallEntries   int
	MaxPermissionEntries int
}

type ProtocolErrorHandler func(ctx Context, event ProtocolErrorEvent)

type ProtocolErrorEvent struct {
	Method string
	Err    error
	Raw    json.RawMessage
}

// ClaudeCodeOptions is the structured form of the Claude Code ACP agent's
// _meta.claudeCode.options. Construct it with CreateClaudeCodeOptions and pass
// the result as StartSessionOptions.Meta to apply at session creation. These
// fields map to the underlying Claude Agent SDK options consumed by
// claude-agent-acp on session/new.
type ClaudeCodeOptions struct {
	// Tools explicitly enumerates the only tools the agent may use. An empty
	// (non-nil) slice disables all built-in tools. When nil, the agent's
	// default tool preset is used.
	Tools []string
	// DisallowedTools removes tools from the model's context entirely (the
	// model never sees them). Example: []string{"WebFetch", "WebSearch"}.
	DisallowedTools []string
	// AllowedTools marks tools that run without a permission prompt. Example:
	// []string{"Bash(echo:*)", "Read"}. Does not remove other tools.
	AllowedTools []string
	// Settings is forwarded to the agent's settings object, supporting the
	// Claude Code permissions schema, e.g. {"permissions":{"deny":["WebFetch"]}}.
	Settings map[string]any
}

// AgentConfig is a cross-agent unified configuration abstraction. Only non-zero
// fields take effect. The profile layer (ApplyAgentConfig hook) translates each
// field into the agent's native format:
//   - Claude Code → _meta.claudeCode.options (disallowedTools, settings, etc.)
//   - Codex       → CODEX_CONFIG env JSON (sandbox_mode, approval_policy)
//   - OpenCode    → InitialConfig model + opencode.json permission (via WriteOpenCodeConfig)
//   - other/unknown → best-effort via _meta + InitialConfig
//
// AgentConfig is additive to InitialConfig and Meta: it does not replace them.
// Precedence: SystemPrompt meta < AgentConfig meta < explicit Meta.
type AgentConfig struct {
	Model           string           // model name (claude: sonnet, codex: gpt-5.5, opencode: glm-5.2)
	Sandbox         string           // sandbox level: read-only / workspace-write / full-access
	DisallowedTools []string         // tools to remove from the model's context entirely
	AllowedTools    []string         // tools that run without a permission prompt
	Permissions     PermissionConfig // unified permission policy
	Extra           map[string]any   // agent-specific native fields (pass-through, no translation)
}

// PermissionConfig is the cross-agent permission policy. Each agent translates
// these into its native permission format (Claude settings.permissions,
// OpenCode opencode.json permission, etc.).
type PermissionConfig struct {
	Allow []string // rules that always pass
	Deny  []string // rules that always block
	Ask   []string // rules that always prompt
}

// CodexConfig is the typed form of Codex's CODEX_CONFIG env var (JSON deep-merged
// into the Codex session config). Construct with CreateCodexConfig and pass the
// result as Agent.Env.
type CodexConfig struct {
	Model          string         // e.g. "deepseek-chat", "gpt-5.5"
	SandboxMode    string         // read-only / workspace-write / danger-full-access
	ApprovalPolicy string         // never / on-request / untrusted / unless-trusted
	WritableRoots  []string       // additional writable paths in workspace-write mode
	NetworkAccess  *bool          // network access in workspace-write sandbox (nil = default)
	Extra          map[string]any // additional native fields merged into CODEX_CONFIG JSON
}

// OpenCodeConfig is the typed form of OpenCode's opencode.json config. Because
// OpenCode does not read _meta or env for permissions, WriteOpenCodeConfig writes
// the file to CWD before session creation.
type OpenCodeConfig struct {
	Model      string             // model id
	Provider   string             // provider id
	Permission OpenCodePermission // permission policy
	Extra      map[string]any     // additional native fields in opencode.json
}

// OpenCodePermission maps to opencode.json's permission key.
type OpenCodePermission struct {
	Allow []string // allowed tool patterns
	Deny  []string // denied tool patterns
	Ask   []string // always-ask tool patterns
}

type StartSessionOptions struct {
	Agent                 Agent
	AgentID               string
	CWD                   string
	MCPServers            []MCPServer
	AdditionalDirectories []string
	SystemPrompt          *SystemPrompt
	InitialConfig         InitialConfig
	Queue                 QueuePolicyInput
	Handlers              AuthorityHandlers
	// Meta is merged into the session _meta object sent on session/new (Create)
	// and session/resume (Resume). SystemPrompt-derived and AgentConfig-derived
	// meta are merged first; explicit Meta wins on conflict. Use this to pass
	// agent-specific structured configuration (e.g. Claude Code's
	// _meta.claudeCode.options to disable tools). Load/Fork do not send _meta.
	Meta map[string]any
	// AgentConfig is a unified, cross-agent configuration abstraction. When set,
	// the profile layer translates it into the agent's native format (env,
	// _meta, CLI flags) automatically for Create and Resume. It is additive to
	// InitialConfig and Meta: model/sandbox/tool settings from AgentConfig are
	// applied in addition to (not instead of) InitialConfig. Precedence on
	// _meta: SystemPrompt < AgentConfig < explicit Meta. nil = no agent config
	// applied.
	AgentConfig *AgentConfig
}

type LoadSessionOptions struct {
	StartSessionOptions
	SessionID string
}

type ResumeSessionOptions = LoadSessionOptions
type ForkSessionOptions = LoadSessionOptions

type ListSessionsOptions struct {
	Agent                 Agent
	AgentID               string
	CWD                   string
	Cursor                *string
	Source                string
	AdditionalDirectories []string
	Handlers              AuthorityHandlers
}

type SystemPrompt struct {
	Text string
}

type InitialConfig struct {
	Mode   any
	Model  any
	Effort any
	Raw    map[string]any
}

type InitialConfigReport struct {
	Applied []InitialConfigReportItem
	Skipped []InitialConfigReportItem
}

type InitialConfigReportItem struct {
	Key    string
	ID     string
	Value  any
	Reason string
}

type QueuePolicyInput struct {
	Delivery string
}

type QueuePolicy struct {
	Delivery string `json:"delivery"`
}

type RuntimeSessionList struct {
	Sessions   []RuntimeSessionReference
	NextCursor *string
}

type RuntimeSessionReference struct {
	ID        string
	AgentType string
	CWD       string
	Title     string
	UpdatedAt string
	Source    string
}

type RuntimeCapabilities struct {
	CanLoadSession     bool
	CanListSessions    bool
	CanForkSession     bool
	CanResumeSession   bool
	CanCloseSession    bool
	PromptCapabilities PromptCapabilities
}

type RuntimeDiagnostics struct {
	Warnings []string
	Raw      map[string]any
}

type RuntimeSessionMetadata struct {
	SessionID          string
	Title              string
	UpdatedAt          string
	AgentModes         []RuntimeAgentMode
	AgentModels        []RuntimeAgentModel
	CurrentModeID      string
	AgentConfigOptions []RuntimeAgentConfigOption
	AvailableCommands  []AvailableCommand
}

type RuntimeAgentMode struct {
	ID          string
	Name        string
	Description string
}

type RuntimeAgentModel struct {
	ID          string
	Name        string
	Description string
}

type RuntimeAgentConfigOption struct {
	Type        string
	ID          string
	Name        string
	Description string
	Category    string
	Value       any
	Options     []SessionConfigChoice
}

type AvailableCommand struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Input       any    `json:"input,omitempty"`
}

type RuntimeSnapshot struct {
	Version       int                    `json:"version"`
	Agent         Agent                  `json:"agent"`
	CWD           string                 `json:"cwd"`
	MCPServers    []MCPServer            `json:"mcpServers,omitempty"`
	Session       RuntimeSnapshotSession `json:"session"`
	CurrentModeID string                 `json:"currentModeId,omitempty"`
	RawConfig     map[string]any         `json:"rawConfig,omitempty"`
}

type RuntimeSnapshotSession struct {
	ID string `json:"id"`
}

type RuntimePrompt struct {
	Messages []RuntimePromptMessage
	Text     string
	Parts    []ContentBlock
}

type RuntimePromptMessage struct {
	Role  string
	Parts []ContentBlock
}

type TurnHandle struct {
	TurnID     string
	Events     <-chan TurnEvent
	Completion <-chan TurnResult
}

type TurnResult struct {
	Completion TurnCompletion
	Err        error
}

type TurnCompletion struct {
	TurnID     string
	OutputText string
	StopReason string
	Usage      *Usage
}

type TurnEvent struct {
	Type       string
	TurnID     string
	Text       string
	Thinking   string
	Plan       []PlanEntry
	ToolCall   *ToolCallSnapshot
	Operation  *Operation
	Permission *PermissionRequestSnapshot
	Usage      *Usage
	Error      error
	Completion *TurnCompletion
}

type ThreadEntry struct {
	ID        string
	Kind      string
	Status    string
	Text      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type DiffSnapshot struct {
	Path      string
	OldText   string
	NewText   string
	UpdatedAt time.Time
}

type ToolCallSnapshot struct {
	ID        string
	Title     string
	Kind      string
	Status    string
	Target    string
	Content   []ContentBlock
	RawInput  json.RawMessage
	RawOutput json.RawMessage
	UpdatedAt time.Time
}

type Operation struct {
	ID        string
	Kind      string
	Phase     string
	Title     string
	Target    OperationTarget
	UpdatedAt time.Time
}

type OperationTarget struct {
	Type  string
	Value string
}

type PermissionRequestSnapshot struct {
	ID        string
	Phase     string
	Operation string
	Request   PermissionRequest
}
