package acpruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

// nativeEngine is the per-CLI translation surface. Implementations spawn the
// real CLI and speak its native protocol; the bridge in this file emulates the
// ACP agent role around them, so the entire host-side stack (SessionService,
// acpSessionDriver, read models, authority callbacks) is reused unchanged.
type nativeEngineOptions struct {
	Agent Agent
	CWD   string
	// nativeInfo (optional) is filled by the bridge with engine/version probe
	// results and surfaced to hosts through RuntimeDiagnostics.
	nativeInfo map[string]any
	// emit delivers one ACP session/update entry for the given session.
	emit func(sessionID string, update SessionUpdate)
	// requestPermission surfaces engine-native permission prompts (e.g.
	// claude's can_use_tool control request) as an ACP
	// session/request_permission round-trip to the host authority.
	requestPermission func(ctx context.Context, req PermissionRequest) (PermissionDecision, error)
}

type nativeTurnResult struct {
	StopReason string
	Usage      *Usage
}

type nativeEngine interface {
	// Name returns the engine id used for version-floor lookups ("codex",
	// "claude").
	Name() string
	// Start spawns the engine process and completes its native handshake.
	// Called once, while servicing the ACP initialize request; an error here
	// fails StartSession with a clear process error.
	Start(ctx context.Context, opts nativeEngineOptions) error
	// NewSession opens an engine session and returns its id (becomes the ACP
	// sessionId). The request carries host metadata (system prompt, model,
	// permissions, MCP servers) so engines that configure via spawn flags
	// (claude) can launch with them; engines configured via env (codex) may
	// ignore it.
	NewSession(ctx context.Context, opts nativeEngineOptions, req NewSessionRequest) (string, error)
	// Prompt drives one turn to completion with the full ACP content blocks
	// (text, images, resource links). Updates stream through opts.emit as
	// they arrive.
	Prompt(ctx context.Context, opts nativeEngineOptions, sessionID string, blocks []ContentBlock) (nativeTurnResult, error)
	// LoadSession re-attaches to a PREVIOUS engine conversation (ACP
	// session/load and session/resume). claude respawns with --resume; codex
	// issues thread/resume on its persistent app-server process. Returns the
	// ACP session id (normally the requested one).
	LoadSession(ctx context.Context, opts nativeEngineOptions, req LoadSessionRequest) (string, error)
	// SessionConfigOptions advertises supported startup knobs. InitialConfig is
	// resolved before launching a session; subsequent assignments are readback
	// checks or explicit requires-new-session errors, never pretend hot updates.
	SessionConfigOptions() []SessionConfigOption
	// Cancel best-effort interrupts the in-flight Prompt.
	Cancel(ctx context.Context, opts nativeEngineOptions, sessionID string)
	// Close terminates the engine process and any transport state.
	Close(ctx context.Context) error
}

// nativeInfoMeta probes the engine version once and compares it with the
// verified floor. The result travels in the initialize _meta so the driver can
// surface it through Session.Diagnostics().
func (b *nativeBridge) nativeInfoMeta(ctx context.Context) map[string]any {
	info := b.opts.nativeInfo
	if info == nil {
		info = map[string]any{}
	}
	info["engine"] = b.engine.Name()
	version, err := ProbeNativeEngineVersion(ctx, b.opts.Agent.Command)
	if err != nil {
		info["versionProbeError"] = err.Error()
	} else {
		info["version"] = version
		if floor, ok := NativeEngineVerifiedVersions[b.engine.Name()]; ok {
			info["verifiedFloor"] = floor
			if compareVersions(version, floor) < 0 {
				info["belowVerifiedFloor"] = true
			}
		}
	}
	return map[string]any{"x-acp-runtime-native": info}
}

// errNativeUnsupported is returned for ACP methods the native transport does
// not translate (yet). It is a spec-valid JSON-RPC error, so hosts see an
// honest capability gap instead of a hang.
func errNativeUnsupported(method string) error {
	return &RuntimeError{
		Kind: ErrorProtocol,
		Op:   "native." + method,
		Msg:  fmt.Sprintf("%s is not supported by the native transport yet", method),
	}
}

// newNativeBridgeConnection wires a loopback ACP agent around a nativeEngine.
//
//	acpSessionDriver --ACP JSON-RPC--> host Peer --io.Pipe--> bridge Peer (agent role)
//	                                                                  | native protocol
//	                                                                  v
//	                                                              engine CLI
//
// The host side reuses Peer + Connection untouched; the bridge registers the
// ACP agent-role methods and translates them to the engine interface.
func newNativeBridgeConnection(ctx context.Context, input ConnectionFactoryInput, makeEngine func() nativeEngine) (ConnectionHandle, error) {
	hostRead, bridgeWrite := io.Pipe() // bridge -> host
	bridgeRead, hostWrite := io.Pipe() // host -> bridge

	bridgeCtx, cancelBridge := context.WithCancel(context.WithoutCancel(ctx))
	engine := makeEngine()

	bridge := &nativeBridge{
		engine: engine,
		cancel: cancelBridge,
	}
	bridge.opts = nativeEngineOptions{
		Agent: input.Agent,
		CWD:   input.CWD,
		emit: func(sessionID string, update SessionUpdate) {
			_ = bridge.peer.Notify(bridgeCtx, "session/update", SessionNotification{SessionID: sessionID, Update: update})
		},
		requestPermission: func(ctx context.Context, req PermissionRequest) (PermissionDecision, error) {
			var resp PermissionDecision
			err := bridge.peer.Call(ctx, "session/request_permission", req, &resp)
			if err != nil {
				return PermissionDecision{}, err
			}
			return resp, nil
		},
	}

	hostPeer := NewPeer(hostRead, hostWrite, PeerOptions{})
	bridge.peer = NewPeer(bridgeRead, bridgeWrite, PeerOptions{})
	registerNativeBridgeHandlers(bridge)

	conn := NewConnectionWithObservability(hostPeer, input.Client, input.Observability)
	go func() { _ = hostPeer.Start(bridgeCtx) }()
	go func() { _ = bridge.peer.Start(bridgeCtx) }()

	var disposeMu sync.Mutex
	disposed := false
	dispose := func(ctx context.Context) error {
		disposeMu.Lock()
		defer disposeMu.Unlock()
		if disposed {
			return nil
		}
		cancelBridge()
		_ = hostRead.Close()
		_ = hostWrite.Close()
		_ = bridgeRead.Close()
		_ = bridgeWrite.Close()
		hostPeer.Close()
		bridge.peer.Close()
		if err := engine.Close(ctx); err != nil {
			return err
		}
		disposed = true
		return nil
	}
	return ConnectionHandle{Connection: conn, Dispose: dispose}, nil
}

type nativeBridge struct {
	engine nativeEngine
	opts   nativeEngineOptions
	peer   *Peer
	cancel context.CancelFunc
}

func registerNativeBridgeHandlers(b *nativeBridge) {
	p := b.peer

	p.RegisterRequest("initialize", func(ctx context.Context, _ json.RawMessage) (any, error) {
		if err := b.engine.Start(ctx, b.opts); err != nil {
			return nil, err
		}
		// Advertise exactly what the bridge translates: prompt turns and
		// load/resume. Everything else answers with a spec-valid error.
		return InitializeResponse{
			ProtocolVersion: ProtocolVersion,
			AgentInfo:       &Implementation{Name: "acp-runtime-native", Version: "0.1.0"},
			AgentCapabilities: AgentCapabilities{
				LoadSession:         true,
				SessionCapabilities: SessionCapabilities{Resume: map[string]any{}},
			},
			Meta: b.nativeInfoMeta(ctx),
		}, nil
	})

	nativeInfo := map[string]any{}
	b.opts.nativeInfo = nativeInfo

	p.RegisterRequest("session/new", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var req NewSessionRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, err
		}
		id, err := b.engine.NewSession(ctx, b.opts, req)
		if err != nil {
			return nil, err
		}
		return nativeSessionResponse(b.engine, id), nil
	})

	// set_config_option / set_mode: engines with spawn-time knobs (claude:
	// model/permission-mode) accept them; others answer unsupported.
	setConfigOption := func(ctx context.Context, raw json.RawMessage) (any, error) {
		var req struct {
			SessionID string `json:"sessionId"`
			OptionID  string `json:"configId"`
			Value     any    `json:"value"`
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, err
		}
		setter, ok := b.engine.(interface {
			SetSpawnOption(sessionID string, key string, value any) error
		})
		if !ok {
			return nil, errNativeUnsupported("session/set_config_option")
		}
		if err := setter.SetSpawnOption(req.SessionID, req.OptionID, req.Value); err != nil {
			return nil, err
		}
		options := nativeSessionResponse(b.engine, req.SessionID).ConfigOptions
		if options == nil {
			options = []SessionConfigOption{}
		}
		return SetSessionConfigOptionResponse{ConfigOptions: &options}, nil
	}
	p.RegisterRequest("session/set_config_option", setConfigOption)
	p.RegisterRequest("session/set_mode", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var req struct {
			SessionID string `json:"sessionId"`
			ModeID    string `json:"modeId"`
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, err
		}
		setter, ok := b.engine.(interface {
			SetSpawnOption(sessionID string, key string, value any) error
		})
		if !ok {
			return nil, errNativeUnsupported("session/set_mode")
		}
		if err := setter.SetSpawnOption(req.SessionID, "mode", req.ModeID); err != nil {
			return nil, err
		}
		return SetSessionModeResponse{}, nil
	})

	p.RegisterRequest("session/prompt", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var req PromptRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, err
		}
		result, err := b.engine.Prompt(ctx, b.opts, req.SessionID, req.Prompt)
		if err != nil {
			return nil, err
		}
		return PromptResponse{StopReason: result.StopReason, Usage: result.Usage}, nil
	})

	p.RegisterNotification("session/cancel", func(ctx context.Context, raw json.RawMessage) {
		var req CancelRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return
		}
		b.engine.Cancel(ctx, b.opts, req.SessionID)
	})

	p.RegisterRequest("session/close", func(context.Context, json.RawMessage) (any, error) {
		return emptyResponse{}, nil
	})

	// session/fork: engines that can derive a new lineage from a previous
	// conversation implement ForkSession; others answer unsupported.
	p.RegisterRequest("session/fork", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var req ForkSessionRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, err
		}
		forker, ok := b.engine.(interface {
			ForkSession(ctx context.Context, opts nativeEngineOptions, req ForkSessionRequest) (string, error)
		})
		if !ok {
			return nil, errNativeUnsupported("session/fork")
		}
		id, err := forker.ForkSession(ctx, b.opts, req)
		if err != nil {
			return nil, err
		}
		return nativeSessionResponse(b.engine, id), nil
	})

	// session/load and session/resume share one translation: re-attach the
	// engine to a previous conversation (claude --resume respawn, codex
	// thread/resume). The ACP session id is the host's handle and is
	// preserved.
	loadResume := func(ctx context.Context, raw json.RawMessage) (any, error) {
		var req struct {
			SessionID             string         `json:"sessionId"`
			CWD                   string         `json:"cwd"`
			MCPServers            []MCPServer    `json:"mcpServers"`
			AdditionalDirectories []string       `json:"additionalDirectories"`
			Meta                  map[string]any `json:"_meta"`
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, err
		}
		if req.SessionID == "" {
			return nil, &RuntimeError{Kind: ErrorLoad, Op: "native.load", Msg: "missing sessionId"}
		}
		id, err := b.engine.LoadSession(ctx, b.opts, LoadSessionRequest{
			SessionID:             req.SessionID,
			CWD:                   b.opts.CWD,
			MCPServers:            req.MCPServers,
			AdditionalDirectories: req.AdditionalDirectories,
			Meta:                  req.Meta,
		})
		if err != nil {
			return nil, err
		}
		return nativeSessionResponse(b.engine, id), nil
	}
	p.RegisterRequest("session/load", loadResume)
	p.RegisterRequest("session/resume", loadResume)

	for _, method := range []string{
		"authenticate",
		"session/list", "session/delete", "logout",
	} {
		p.RegisterRequest(method, func(context.Context, json.RawMessage) (any, error) {
			return nil, errNativeUnsupported(method)
		})
	}
}

func nativeSessionResponse(engine nativeEngine, sessionID string) NewSessionResponse {
	if source, ok := engine.(interface {
		SessionState(string) NewSessionResponse
	}); ok {
		return source.SessionState(sessionID)
	}
	return NewSessionResponse{SessionID: sessionID, ConfigOptions: engine.SessionConfigOptions()}
}

// promptBlocksText flattens ACP content blocks to plain text for engines that
// only accept a text prompt.
func promptBlocksText(blocks []ContentBlock) string {
	text := ""
	for _, block := range blocks {
		if block.Type == "text" {
			text += block.Text
		}
	}
	return text
}
