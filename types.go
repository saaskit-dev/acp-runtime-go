package acpruntime

import (
	"encoding/json"
	"time"
)

const (
	ProtocolVersion = 1

	ACPProtocolSourceRepo        = "https://github.com/agentclientprotocol/agent-client-protocol"
	ACPProtocolSourceRef         = "v0.11.4"
	ACPProtocolDocsURL           = "https://agentclientprotocol.com/protocol/overview"
	ACPProtocolDocsSchemaURL     = "https://agentclientprotocol.com/protocol/draft/schema"
	ACPProtocolAlignmentVerified = "2026-04-08"

	RuntimeSnapshotVersion = 1

	RuntimeAuthenticationDefaultMethodMetaKey = "acp-runtime/default-auth-method"
	RuntimeTerminalAuthSuccessPatternsMetaKey = "acp-runtime/terminal-success-patterns"
)

type Agent struct {
	Type    string            `json:"type"`
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
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

type ClientCapabilities struct {
	Terminal bool                       `json:"terminal"`
	FS       FilesystemCapabilities     `json:"fs"`
	Auth     ClientAuthCapabilities     `json:"auth"`
	Meta     map[string]json.RawMessage `json:"_meta,omitempty"`
}

type ClientAuthCapabilities struct {
	Terminal bool `json:"terminal"`
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
	SessionID     string                `json:"sessionId"`
	Modes         *SessionModeState     `json:"modes,omitempty"`
	Models        *SessionModelState    `json:"models,omitempty"`
	ConfigOptions []SessionConfigOption `json:"configOptions,omitempty"`
	Meta          map[string]any        `json:"_meta,omitempty"`
}

type LoadSessionRequest struct {
	SessionID             string      `json:"sessionId"`
	CWD                   string      `json:"cwd"`
	MCPServers            []MCPServer `json:"mcpServers"`
	AdditionalDirectories []string    `json:"additionalDirectories,omitempty"`
}

type LoadSessionResponse = NewSessionResponse

type ResumeSessionRequest struct {
	SessionID             string      `json:"sessionId"`
	CWD                   string      `json:"cwd"`
	MCPServers            []MCPServer `json:"mcpServers"`
	AdditionalDirectories []string    `json:"additionalDirectories,omitempty"`
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

type CancelRequest struct {
	SessionID string `json:"sessionId"`
}

type SetSessionModeRequest struct {
	SessionID string `json:"sessionId"`
	ModeID    string `json:"modeId"`
}

type SetSessionModeResponse struct{}

type SetSessionConfigOptionRequest struct {
	SessionID string `json:"sessionId"`
	OptionID  string `json:"optionId"`
	Value     any    `json:"value"`
}

type SetSessionConfigOptionResponse struct{}

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
	Value       any                   `json:"value,omitempty"`
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
	SessionUpdate  string                `json:"sessionUpdate"`
	Type           string                `json:"type,omitempty"`
	Text           string                `json:"text,omitempty"`
	ToolCallID     string                `json:"toolCallId,omitempty"`
	Title          *string               `json:"title,omitempty"`
	Kind           *string               `json:"kind,omitempty"`
	Status         *string               `json:"status,omitempty"`
	Locations      []ToolLocation        `json:"locations,omitempty"`
	Content        ContentBlocks         `json:"content,omitempty"`
	RawInput       json.RawMessage       `json:"rawInput,omitempty"`
	RawOutput      json.RawMessage       `json:"rawOutput,omitempty"`
	Entries        []PlanEntry           `json:"entries,omitempty"`
	CurrentModeID  string                `json:"currentModeId,omitempty"`
	AvailableModes []SessionMode         `json:"availableModes,omitempty"`
	ConfigOptions  []SessionConfigOption `json:"configOptions,omitempty"`
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

type TerminalHandler interface {
	CreateTerminal(ctx Context, request TerminalStartRequest) (TerminalSnapshot, error)
	Output(ctx Context, terminalID string) (TerminalSnapshot, error)
	Wait(ctx Context, terminalID string) (TerminalSnapshot, error)
	Kill(ctx Context, terminalID string) (TerminalSnapshot, error)
	Release(ctx Context, terminalID string) (TerminalSnapshot, error)
}

type TerminalStartRequest struct {
	Command string
	Args    []string
	CWD     string
	Env     map[string]string
}

type TerminalSnapshot struct {
	ID        string
	Command   string
	Status    string
	Output    string
	ExitCode  *int
	StartedAt time.Time
	UpdatedAt time.Time
}

type ObservabilityOptions struct {
	CaptureContent  string
	OnProtocolError ProtocolErrorHandler
}

type ProtocolErrorHandler func(ctx Context, event ProtocolErrorEvent)

type ProtocolErrorEvent struct {
	Method string
	Err    error
	Raw    json.RawMessage
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
	CurrentModeID      string
	AgentConfigOptions []RuntimeAgentConfigOption
	AvailableCommands  []AvailableCommand
}

type RuntimeAgentMode struct {
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
