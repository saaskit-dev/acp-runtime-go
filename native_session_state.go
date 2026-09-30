package acpruntime

import (
	"encoding/json"
	"fmt"
)

func nativeCodexModel(agent Agent, meta map[string]any) string {
	if model := metaString(meta, "model"); model != "" {
		return model
	}
	var config struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal([]byte(agent.Env["CODEX_CONFIG"]), &config)
	return config.Model
}

func nativeModelOption(model string) SessionConfigOption {
	category := "model"
	return SessionConfigOption{Type: "select", ID: "model", Name: "Model", Category: &category, Value: model, Options: []SessionConfigChoice{{Value: model, Name: firstNonEmpty(model, "Provider default")}}}
}

func (e *codexNativeEngine) SessionState(sessionID string) NewSessionResponse {
	e.mu.Lock()
	defer e.mu.Unlock()
	response := NewSessionResponse{SessionID: sessionID}
	if session := e.sessions[sessionID]; session != nil {
		response.ConfigOptions = []SessionConfigOption{nativeModelOption(session.model)}
		response.Models = &SessionModelState{CurrentModelID: session.model}
		if session.model != "" {
			response.Models.AvailableModels = []ModelInfo{{ID: session.model, Name: session.model}}
		}
	}
	return response
}

// Codex's native boot config is immutable. Equal assignments acknowledge the
// actual thread/start readback; other changes require a fresh connection.
func (e *codexNativeEngine) SetSpawnOption(sessionID, key string, value any) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if session := e.sessions[sessionID]; session != nil && key == "model" && value == session.model {
		return nil
	}
	return wrapError(ErrorProtocol, "native.codex.config", fmt.Sprintf("%s is fixed at session start; create a new session to change it", key), nil)
}

func (e *claudeNativeEngine) SessionState(sessionID string) NewSessionResponse {
	proc, err := e.proc(sessionID)
	if err != nil {
		return NewSessionResponse{SessionID: sessionID}
	}
	proc.mu.Lock()
	defer proc.mu.Unlock()
	model, mode := proc.model, proc.permissionMode
	if proc.pendingReq != nil {
		if model == "" {
			model = metaString(proc.pendingReq.Meta, "model")
		}
		if mode == "" {
			mode = metaString(proc.pendingReq.Meta, "mode")
		}
	}
	if model == "" {
		model = e.opts.Agent.Env["ANTHROPIC_MODEL"]
	}
	if mode == "" {
		mode = "default"
	}
	category := "mode"
	models := &SessionModelState{CurrentModelID: model}
	if model != "" {
		models.AvailableModels = []ModelInfo{{ID: model, Name: model}}
	}
	return NewSessionResponse{
		SessionID: sessionID,
		Models:    models,
		Modes: &SessionModeState{
			CurrentModeID: mode,
			AvailableModes: []SessionMode{
				{ID: "default", Name: "Default"},
				{ID: "acceptEdits", Name: "Accept Edits"},
				{ID: "plan", Name: "Plan"},
				{ID: "bypassPermissions", Name: "Bypass Permissions"},
				{ID: "dontAsk", Name: "Don't Ask"},
			},
		},
		ConfigOptions: []SessionConfigOption{
			nativeModelOption(model),
			{Type: "select", ID: "mode", Name: "Permission mode", Category: &category, Value: mode, Options: claudeModeChoices()},
		},
	}
}

func claudeModeChoices() []SessionConfigChoice {
	var choices []SessionConfigChoice
	for _, mode := range []string{"default", "acceptEdits", "plan", "bypassPermissions", "dontAsk"} {
		choices = append(choices, SessionConfigChoice{Value: mode, Name: mode})
	}
	return choices
}
