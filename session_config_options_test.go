package acpruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestConfigOptionUpdateReplacesFullSnapshot(t *testing.T) {
	driver := configOptionTestDriver([]SessionConfigOption{
		configOption("model", "Model", "supported"),
		configOption("fast", "Fast", "enabled"),
	})

	driver.handleSessionUpdate(SessionNotification{
		SessionID: "session-1",
		Update: SessionUpdate{
			SessionUpdate: "config_option_update",
			ConfigOptions: []SessionConfigOption{
				configOption("model", "Model", "deepseek-v4-pro"),
			},
		},
	})

	got := driver.Metadata().AgentConfigOptions
	if len(got) != 1 || got[0].ID != "model" || got[0].Value != "deepseek-v4-pro" {
		t.Fatalf("AgentConfigOptions = %#v, want only updated model option", got)
	}
}

func TestConfigOptionUpdateEmptySnapshotClearsOptions(t *testing.T) {
	driver := configOptionTestDriver([]SessionConfigOption{
		configOption("model", "Model", "supported"),
		configOption("fast", "Fast", "enabled"),
	})

	driver.handleSessionUpdate(SessionNotification{
		SessionID: "session-1",
		Update: SessionUpdate{
			SessionUpdate: "config_option_update",
			ConfigOptions: []SessionConfigOption{},
		},
	})

	got := driver.Metadata().AgentConfigOptions
	if got == nil || len(got) != 0 {
		t.Fatalf("AgentConfigOptions = %#v, want explicit empty snapshot", got)
	}
}

func TestConfigOptionUpdatePreservesProviderOrderAndValuesWithoutDuplicates(t *testing.T) {
	driver := configOptionTestDriver([]SessionConfigOption{
		configOption("model", "Old Model", "old"),
		configOption("fast", "Old Fast", "old"),
		configOption("model", "Duplicate Model", "duplicate"),
	})
	want := []SessionConfigOption{
		configOption("fast", "Fast", "disabled"),
		configOption("model", "Model", "supported"),
		configOption("thought", "Thought", "high"),
	}

	driver.handleSessionUpdate(SessionNotification{
		SessionID: "session-1",
		Update: SessionUpdate{
			SessionUpdate: "config_option_update",
			ConfigOptions: want,
		},
	})

	got := driver.Metadata().AgentConfigOptions
	wantRuntime := make([]RuntimeAgentConfigOption, 0, len(want))
	for _, option := range want {
		wantRuntime = append(wantRuntime, runtimeConfigOptionFromACP(option))
	}
	if !reflect.DeepEqual(got, wantRuntime) {
		t.Fatalf("AgentConfigOptions = %#v, want provider snapshot %#v", got, wantRuntime)
	}
}

func TestSetAgentConfigOptionAppliesResponseSnapshotBeforeReturn(t *testing.T) {
	var mu sync.Mutex
	var calls []SetSessionConfigOptionRequest
	driver := configOptionTestDriver([]SessionConfigOption{
		configOption("model", "Model", "supported"),
		configOption("fast", "Fast", "disabled"),
	})
	connectConfigOptionProvider(t, driver, func(req SetSessionConfigOptionRequest) SetSessionConfigOptionResponse {
		mu.Lock()
		calls = append(calls, req)
		mu.Unlock()

		var options []SessionConfigOption
		switch {
		case req.OptionID == "model" && req.Value == "deepseek-v4-pro":
			options = []SessionConfigOption{
				configOption("model", "Model", "deepseek-v4-pro"),
			}
		case req.OptionID == "model" && req.Value == "supported":
			options = []SessionConfigOption{
				configOption("model", "Model", "supported"),
				configOption("fast", "Fast", "disabled"),
			}
		case req.OptionID == "fast" && req.Value == "enabled":
			options = []SessionConfigOption{
				configOption("model", "Model", "supported"),
				configOption("fast", "Fast", "enabled"),
			}
		default:
			t.Fatalf("unexpected set_config_option request: %#v", req)
		}
		return SetSessionConfigOptionResponse{ConfigOptions: &options}
	})

	if err := driver.SetAgentConfigOption(context.Background(), "model", "deepseek-v4-pro"); err != nil {
		t.Fatalf("SetAgentConfigOption(deepseek-v4-pro) error = %v", err)
	}
	got := driver.Metadata().AgentConfigOptions
	if len(got) != 1 || got[0].ID != "model" {
		t.Fatalf("options after deepseek-v4-pro = %#v, want Fast removed before return", got)
	}

	if err := driver.SetAgentConfigOption(context.Background(), "model", "supported"); err != nil {
		t.Fatalf("SetAgentConfigOption(supported) error = %v", err)
	}
	got = driver.Metadata().AgentConfigOptions
	if len(got) != 2 || got[0].ID != "model" || got[1].ID != "fast" || got[1].Value != "disabled" {
		t.Fatalf("options after supported model = %#v, want model then restored Fast", got)
	}

	if err := driver.SetAgentConfigOption(context.Background(), "fast", "enabled"); err != nil {
		t.Fatalf("SetAgentConfigOption(fast) error = %v", err)
	}
	got = driver.Metadata().AgentConfigOptions
	if len(got) != 2 || got[1].ID != "fast" || got[1].Value != "enabled" {
		t.Fatalf("options after enabling Fast = %#v, want response snapshot value", got)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 3 {
		t.Fatalf("set_config_option calls = %#v, want three successful calls", calls)
	}
}

func TestSetAgentConfigOptionExplicitEmptyResponseClearsOptions(t *testing.T) {
	driver := configOptionTestDriver([]SessionConfigOption{
		configOption("model", "Model", "supported"),
		configOption("fast", "Fast", "disabled"),
	})
	connectConfigOptionProvider(t, driver, func(SetSessionConfigOptionRequest) SetSessionConfigOptionResponse {
		options := []SessionConfigOption{}
		return SetSessionConfigOptionResponse{ConfigOptions: &options}
	})

	if err := driver.SetAgentConfigOption(context.Background(), "model", "deepseek-v4-pro"); err != nil {
		t.Fatalf("SetAgentConfigOption() error = %v", err)
	}
	got := driver.Metadata().AgentConfigOptions
	if got == nil || len(got) != 0 {
		t.Fatalf("AgentConfigOptions = %#v, want explicit empty response snapshot", got)
	}
}

func TestSetAgentConfigOptionMissingResponseSnapshotUsesLegacyUpdate(t *testing.T) {
	driver := configOptionTestDriver([]SessionConfigOption{
		configOption("model", "Model", "supported"),
		configOption("fast", "Fast", "disabled"),
	})
	connectConfigOptionProvider(t, driver, func(SetSessionConfigOptionRequest) SetSessionConfigOptionResponse {
		return SetSessionConfigOptionResponse{}
	})

	if err := driver.SetAgentConfigOption(context.Background(), "fast", "enabled"); err != nil {
		t.Fatalf("SetAgentConfigOption() error = %v", err)
	}
	got := driver.Metadata().AgentConfigOptions
	if len(got) != 2 || got[1].ID != "fast" || got[1].Value != "enabled" {
		t.Fatalf("AgentConfigOptions = %#v, want legacy in-place value update", got)
	}
}

func TestSetSessionConfigOptionResponseDistinguishesMissingAndEmptySnapshot(t *testing.T) {
	var missing SetSessionConfigOptionResponse
	if err := json.Unmarshal([]byte(`{}`), &missing); err != nil {
		t.Fatalf("Unmarshal(missing) error = %v", err)
	}
	if missing.ConfigOptions != nil {
		t.Fatalf("missing ConfigOptions = %#v, want nil", missing.ConfigOptions)
	}

	var empty SetSessionConfigOptionResponse
	if err := json.Unmarshal([]byte(`{"configOptions":[]}`), &empty); err != nil {
		t.Fatalf("Unmarshal(empty) error = %v", err)
	}
	if empty.ConfigOptions == nil || *empty.ConfigOptions == nil || len(*empty.ConfigOptions) != 0 {
		t.Fatalf("empty ConfigOptions = %#v, want present empty slice", empty.ConfigOptions)
	}
}

func TestSessionConfigOptionUsesACPCurrentValueWireField(t *testing.T) {
	var selectOption SessionConfigOption
	if err := json.Unmarshal([]byte(`{"type":"select","id":"model","name":"Model","currentValue":"haiku","options":[]}`), &selectOption); err != nil {
		t.Fatalf("Unmarshal(select) error = %v", err)
	}
	if selectOption.Value != "haiku" {
		t.Fatalf("select Value = %#v, want haiku", selectOption.Value)
	}

	var booleanOption SessionConfigOption
	if err := json.Unmarshal([]byte(`{"type":"boolean","id":"fast","name":"Fast mode","currentValue":false}`), &booleanOption); err != nil {
		t.Fatalf("Unmarshal(boolean) error = %v", err)
	}
	if value, ok := booleanOption.Value.(bool); !ok || value {
		t.Fatalf("boolean Value = %#v, want false", booleanOption.Value)
	}

	wire, err := json.Marshal(booleanOption)
	if err != nil {
		t.Fatalf("Marshal(boolean) error = %v", err)
	}
	var encoded map[string]any
	if err := json.Unmarshal(wire, &encoded); err != nil {
		t.Fatalf("Unmarshal(encoded) error = %v", err)
	}
	if value, ok := encoded["currentValue"].(bool); !ok || value {
		t.Fatalf("encoded currentValue = %#v, want false", encoded["currentValue"])
	}
	if _, exists := encoded["value"]; exists {
		t.Fatalf("encoded legacy value field = %#v, want absent", encoded["value"])
	}
}

func TestSessionDriverHydratesAndRefreshesRawConfigFromProviderSnapshots(t *testing.T) {
	response := NewSessionResponse{
		SessionID: "session-1",
		Modes:     &SessionModeState{CurrentModeID: "bypassPermissions"},
		ConfigOptions: []SessionConfigOption{
			configOption("model", "Model", "haiku"),
			{Type: "boolean", ID: "fast", Name: "Fast mode", Value: false},
		},
	}
	metadata := metadataFromSessionResponse(response)
	driver := &acpSessionDriver{
		sessionID: "session-1",
		metadata:  metadata,
		rawConfig: rawConfigFromMetadata(metadata),
	}

	want := map[string]any{"mode": "bypassPermissions", "model": "haiku", "fast": false}
	if got := driver.Snapshot().RawConfig; !reflect.DeepEqual(got, want) {
		t.Fatalf("initial RawConfig = %#v, want %#v", got, want)
	}

	driver.handleSessionUpdate(SessionNotification{SessionID: "session-1", Update: SessionUpdate{
		SessionUpdate: "config_option_update",
		ConfigOptions: []SessionConfigOption{
			configOption("model", "Model", "sonnet"),
		},
	}})
	want = map[string]any{"mode": "bypassPermissions", "model": "sonnet"}
	if got := driver.Snapshot().RawConfig; !reflect.DeepEqual(got, want) {
		t.Fatalf("updated RawConfig = %#v, want %#v", got, want)
	}
}

// initialConfigProvider uses the real JSON-RPC connection, but no processes or
// external provider. Its mutex also protects assertions against RPC goroutines.
type initialConfigProvider struct {
	mu       sync.Mutex
	response NewSessionResponse
	calls    []string
	change   func(string, any) error // called under mu, before the response
	legacy   bool
}

func (p *initialConfigProvider) connect(t *testing.T) (*SessionService, *Peer) {
	t.Helper()
	driver := configOptionTestDriver(nil)
	peer := connectConfigOptionProvider(t, driver, func(SetSessionConfigOptionRequest) SetSessionConfigOptionResponse {
		panic("handler replaced below")
	})
	peer.RegisterRequest("initialize", func(context.Context, json.RawMessage) (any, error) {
		return InitializeResponse{}, nil
	})
	for _, method := range []string{"session/new", "session/resume", "session/load", "session/fork"} {
		peer.RegisterRequest(method, func(context.Context, json.RawMessage) (any, error) {
			p.mu.Lock()
			defer p.mu.Unlock()
			return p.response, nil
		})
	}
	for _, method := range []string{"session/close", "session/delete"} {
		peer.RegisterRequest(method, func(context.Context, json.RawMessage) (any, error) {
			return emptyResponse{}, nil
		})
	}
	peer.RegisterRequest("session/set_mode", func(_ context.Context, raw json.RawMessage) (any, error) {
		var req SetSessionModeRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, err
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		p.calls = append(p.calls, "mode="+req.ModeID)
		if p.change != nil {
			if err := p.change("mode", req.ModeID); err != nil {
				return nil, err
			}
		}
		p.response.Modes = &SessionModeState{CurrentModeID: req.ModeID}
		return SetSessionModeResponse{}, nil
	})
	peer.RegisterRequest("session/set_config_option", func(_ context.Context, raw json.RawMessage) (any, error) {
		var req SetSessionConfigOptionRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, err
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		p.calls = append(p.calls, fmt.Sprintf("%s=%v", req.OptionID, req.Value))
		if p.change != nil {
			if err := p.change(req.OptionID, req.Value); err != nil {
				return nil, err
			}
		}
		found := false
		for i := range p.response.ConfigOptions {
			if p.response.ConfigOptions[i].ID == req.OptionID {
				p.response.ConfigOptions[i].Value = req.Value
				found = true
			}
		}
		if !found {
			return nil, errors.New("unknown config option")
		}
		if p.legacy {
			return SetSessionConfigOptionResponse{}, nil
		}
		options := append([]SessionConfigOption{}, p.response.ConfigOptions...)
		return SetSessionConfigOptionResponse{ConfigOptions: &options}, nil
	})
	service := NewSessionService(func(context.Context, ConnectionFactoryInput) (ConnectionHandle, error) {
		return ConnectionHandle{Connection: driver.connection}, nil
	}, RuntimeOptions{})
	return service, peer
}

func TestInitialConfigRPCCounts(t *testing.T) {
	for _, path := range []string{"create", "resume"} {
		for _, tc := range []struct {
			name    string
			config  InitialConfig
			prepare func(*initialConfigProvider)
			want    []string
			wantErr bool
		}{
			{name: "equal", config: InitialConfig{Mode: "yolo", Model: "haiku", Effort: "high", Raw: map[string]any{"fast": false}}},
			{name: "different", config: InitialConfig{Mode: "plan", Model: "sonnet", Effort: "low", Raw: map[string]any{"fast": true}},
				want: []string{"mode=plan", "model=sonnet", "reasoning_effort=low", "fast=true"}},
			{name: "mode resets effort without snapshot", config: InitialConfig{Mode: "plan", Effort: "high"},
				prepare: func(p *initialConfigProvider) {
					p.change = func(string, any) error { p.response.ConfigOptions[1].Value = "low"; return nil }
				},
				want: []string{"mode=plan", "reasoning_effort=high"}},
			{name: "model resets effort", config: InitialConfig{Model: "sonnet", Effort: "high"},
				prepare: func(p *initialConfigProvider) {
					p.change = func(string, any) error { p.response.ConfigOptions[1].Value = "low"; return nil }
				},
				want: []string{"model=sonnet", "reasoning_effort=high"}},
			{name: "model preserves effort", config: InitialConfig{Model: "sonnet", Effort: "high"}, want: []string{"model=sonnet"}},
			{name: "model removes effort", config: InitialConfig{Model: "sonnet", Effort: "high"},
				prepare: func(p *initialConfigProvider) {
					p.change = func(string, any) error { p.response.ConfigOptions = p.response.ConfigOptions[:1]; return nil }
				},
				want: []string{"model=sonnet"}},
			{name: "model changes effort selector", config: InitialConfig{Model: "sonnet", Effort: "high"},
				prepare: func(p *initialConfigProvider) {
					p.change = func(id string, _ any) error {
						if id == "model" {
							category := "thought_level"
							p.response.ConfigOptions[1] = configOption("thinking", "Thinking", "low")
							p.response.ConfigOptions[1].Category = &category
						}
						return nil
					}
				}, want: []string{"model=sonnet", "thinking=high"}},
			{name: "legacy model response", config: InitialConfig{Model: "sonnet", Effort: "high", Raw: map[string]any{"reasoning_effort": "high"}},
				prepare: func(p *initialConfigProvider) { p.legacy = true },
				want:    []string{"model=sonnet", "reasoning_effort=high", "reasoning_effort=high"}},
			{name: "raw sees latest response", config: InitialConfig{Model: "sonnet", Raw: map[string]any{"model": "sonnet"}}, want: []string{"model=sonnet"}},
			{name: "missing options skip", config: InitialConfig{Model: "haiku", Effort: "high"},
				prepare: func(p *initialConfigProvider) { p.response.ConfigOptions = nil }},
			{name: "missing mode sends", config: InitialConfig{Mode: "plan"},
				prepare: func(p *initialConfigProvider) { p.response.Modes = nil }, want: []string{"mode=plan"}},
			{name: "unknown raw errors", config: InitialConfig{Raw: map[string]any{"unknown": "x"}}, want: []string{"unknown=x"}, wantErr: true},
			{name: "nil current not equal", config: InitialConfig{Raw: map[string]any{"fast": nil}},
				prepare: func(p *initialConfigProvider) { p.response.ConfigOptions[2].Value = nil }, want: []string{"fast=<nil>"}},
			{name: "case sensitive values", config: InitialConfig{Model: "HAIKU"}, want: []string{"model=HAIKU"}},
			{name: "structured raw equality", config: InitialConfig{Raw: map[string]any{"fast": map[string]any{"flags": []any{"a", "b"}}}},
				prepare: func(p *initialConfigProvider) {
					p.response.ConfigOptions[2].Value = map[string]any{"flags": []any{"a", "b"}}
				}},
			{name: "wire number type conservative", config: InitialConfig{Raw: map[string]any{"fast": 1}},
				prepare: func(p *initialConfigProvider) { p.response.ConfigOptions[2].Value = float64(1) }, want: []string{"fast=1"}},
			{name: "alias order", config: InitialConfig{Mode: "yolo"},
				prepare: func(p *initialConfigProvider) { p.response.Modes.CurrentModeID = "yolo" }, want: []string{"mode=bypassPermissions"}},
			{name: "alias fallback", config: InitialConfig{Mode: "yolo"},
				prepare: func(p *initialConfigProvider) {
					p.response.Modes.CurrentModeID = "plan"
					p.change = func(_ string, value any) error {
						if value == "bypassPermissions" {
							return errors.New("unsupported mode")
						}
						return nil
					}
				}, want: []string{"mode=bypassPermissions", "mode=yolo"}},
			{name: "unknown mode errors", config: InitialConfig{Mode: "unknown"},
				prepare: func(p *initialConfigProvider) {
					p.change = func(string, any) error { return errors.New("unsupported mode") }
				}, want: []string{"mode=unknown"}, wantErr: true},
			{name: "unknown model errors", config: InitialConfig{Model: "unknown"},
				prepare: func(p *initialConfigProvider) {
					p.change = func(string, any) error { return errors.New("unsupported model") }
				}, want: []string{"model=unknown"}, wantErr: true},
		} {
			t.Run(path+"/"+tc.name, func(t *testing.T) {
				p := &initialConfigProvider{response: NewSessionResponse{
					SessionID: "session-1", Modes: &SessionModeState{CurrentModeID: "bypassPermissions"},
					ConfigOptions: []SessionConfigOption{configOption("model", "Model", "haiku"), configOption("reasoning_effort", "Effort", "high"), {Type: "boolean", ID: "fast", Value: false}},
				}}
				if tc.prepare != nil {
					tc.prepare(p)
				}
				service, _ := p.connect(t)
				input := StartSessionOptions{Agent: Agent{Type: ClaudeCodeACPRegistryID}, InitialConfig: tc.config}
				var driver SessionDriver
				var err error
				if path == "create" {
					driver, err = service.Create(context.Background(), input)
				} else {
					driver, err = service.Resume(context.Background(), ResumeSessionOptions{StartSessionOptions: input, SessionID: "session-1"})
				}
				if (err != nil) != tc.wantErr {
					t.Fatalf("error = %v, wantErr %v", err, tc.wantErr)
				}
				p.mu.Lock()
				defer p.mu.Unlock()
				t.Logf("config RPCs=%d calls=%v", len(p.calls), p.calls)
				if !reflect.DeepEqual(p.calls, tc.want) {
					t.Fatalf("calls = %v, want %v", p.calls, tc.want)
				}
				if err == nil {
					want := metadataFromSessionResponse(p.response)
					if got := driver.Metadata(); !reflect.DeepEqual(got, want) {
						t.Fatalf("metadata = %#v, want %#v", got, want)
					}
				}
			})
		}
	}
}

func TestInitialConfigDoesNotChangeExplicitSettersOrLoadFork(t *testing.T) {
	p := &initialConfigProvider{response: NewSessionResponse{
		SessionID: "session-1", Modes: &SessionModeState{CurrentModeID: "plan"},
		ConfigOptions: []SessionConfigOption{configOption("model", "Model", "haiku")},
	}}
	service, _ := p.connect(t)
	input := StartSessionOptions{InitialConfig: InitialConfig{Mode: "plan", Model: "haiku"}}
	driver, err := service.Create(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if err := driver.SetAgentMode(context.Background(), "plan"); err != nil {
		t.Fatal(err)
	}
	if err := driver.SetAgentConfigOption(context.Background(), "model", "haiku"); err != nil {
		t.Fatal(err)
	}
	for _, load := range []func(context.Context, LoadSessionOptions) (SessionDriver, error){service.Load, service.Fork} {
		_, err := load(context.Background(), LoadSessionOptions{SessionID: "session-1", StartSessionOptions: StartSessionOptions{InitialConfig: InitialConfig{Mode: "different", Model: "different"}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if want := []string{"mode=plan", "model=haiku"}; !reflect.DeepEqual(p.calls, want) {
		t.Fatalf("calls = %v, want %v", p.calls, want)
	}
}

func TestInitialConfigResumeUsesNewProviderSnapshot(t *testing.T) {
	p := &initialConfigProvider{response: NewSessionResponse{SessionID: "session-1", ConfigOptions: []SessionConfigOption{configOption("model", "Model", "haiku")}}}
	service, _ := p.connect(t)
	input := StartSessionOptions{InitialConfig: InitialConfig{Model: "haiku"}}
	if _, err := service.Create(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.response.ConfigOptions[0].Value = "sonnet"
	p.mu.Unlock()
	if _, err := service.Resume(context.Background(), ResumeSessionOptions{SessionID: "session-1", StartSessionOptions: input}); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if want := []string{"model=haiku"}; !reflect.DeepEqual(p.calls, want) {
		t.Fatalf("calls = %v, want %v", p.calls, want)
	}
}

func TestInitialConfigOptionAliasesAndMissingReport(t *testing.T) {
	driver := configOptionTestDriver([]SessionConfigOption{configOption("MODEL", "Model", "haiku")})
	var calls int
	connectConfigOptionProvider(t, driver, func(req SetSessionConfigOptionRequest) SetSessionConfigOptionResponse {
		calls++
		return SetSessionConfigOptionResponse{}
	})
	profile := defaultAgentProfile()
	profile.CreateInitialConfigAliases = func(key string, value any) []any { return []any{"haiku", value} }
	report, err := applyInitialConfig(context.Background(), driver, InitialConfig{Model: "friendly-name", Effort: "high"}, profile)
	if err != nil {
		t.Fatal(err)
	}
	want := []InitialConfigReportItem{{Key: "model", ID: "MODEL", Value: "haiku"}, {Key: "effort", Value: "high", Reason: "option_not_found"}}
	if !reflect.DeepEqual(report.Applied, want) {
		t.Fatalf("report = %#v, want %#v", report.Applied, want)
	}
	if calls != 0 {
		t.Fatalf("calls = %d, want 0", calls)
	}
}

func TestInitialConfigConcurrentSnapshotFence(t *testing.T) {
	for _, first := range []string{"old", "new"} {
		t.Run(first+" response first", func(t *testing.T) {
			driver := configOptionTestDriver([]SessionConfigOption{configOption("effort", "Effort", "high")})
			started := make(chan string, 2)
			release := map[string]chan struct{}{"old": make(chan struct{}), "new": make(chan struct{})}
			connectConfigOptionProvider(t, driver, func(req SetSessionConfigOptionRequest) SetSessionConfigOptionResponse {
				value := req.Value.(string)
				started <- value
				<-release[value]
				options := []SessionConfigOption{configOption("effort", "Effort", "high")}
				return SetSessionConfigOptionResponse{ConfigOptions: &options}
			})
			done := map[string]chan error{"old": make(chan error, 1), "new": make(chan error, 1)}
			for _, value := range []string{"old", "new"} {
				go func() { done[value] <- driver.SetAgentConfigOption(context.Background(), "model", value) }()
				if got := <-started; got != value {
					t.Fatalf("started = %s, want %s", got, value)
				}
			}
			// A notification during overlapping requests has no request identity.
			driver.handleSessionUpdate(SessionNotification{Update: SessionUpdate{SessionUpdate: "config_option_update", ConfigOptions: []SessionConfigOption{configOption("effort", "Effort", "high")}}})
			if initialConfigOptionEqual(driver, "effort", "high") {
				t.Fatal("trusted snapshot during pending changes")
			}
			close(release[first])
			if err := <-done[first]; err != nil {
				t.Fatal(err)
			}
			if initialConfigOptionEqual(driver, "effort", "high") {
				t.Fatal("response trusted while another change is pending")
			}
			last := "new"
			if first == "new" {
				last = "old"
			}
			close(release[last])
			if err := <-done[last]; err != nil {
				t.Fatal(err)
			}
			if initialConfigOptionEqual(driver, "effort", "high") {
				t.Fatalf("overlapping %s response restored snapshot authority", last)
			}
			driver.handleSessionUpdate(SessionNotification{Update: SessionUpdate{SessionUpdate: "config_option_update", ConfigOptions: []SessionConfigOption{configOption("effort", "Effort", "high")}}})
			if !initialConfigOptionEqual(driver, "effort", "high") {
				t.Fatal("idle provider notification did not restore authority")
			}
		})
	}
}

func TestInitialConfigLegacyResponseNeedsProviderReadback(t *testing.T) {
	driver := configOptionTestDriver([]SessionConfigOption{configOption("model", "Model", "haiku"), configOption("effort", "Effort", "high")})
	var mu sync.Mutex
	var calls int
	connectConfigOptionProvider(t, driver, func(SetSessionConfigOptionRequest) SetSessionConfigOptionResponse {
		mu.Lock()
		calls++
		mu.Unlock()
		return SetSessionConfigOptionResponse{}
	})
	if err := driver.SetAgentConfigOption(context.Background(), "model", "sonnet"); err != nil {
		t.Fatal(err)
	}
	if initialConfigOptionEqual(driver, "effort", "high") {
		t.Fatal("legacy response trusted stale effort")
	}
	driver.handleSessionUpdate(SessionNotification{Update: SessionUpdate{SessionUpdate: "config_option_update", ConfigOptions: []SessionConfigOption{configOption("effort", "Effort", "high")}}})
	if _, err := applyInitialConfig(context.Background(), driver, InitialConfig{Effort: "high"}, defaultAgentProfile()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("calls = %d, want only the explicit model RPC", calls)
	}
}

func TestInitialConfigModeNotificationRequiresConfigReadback(t *testing.T) {
	for _, effort := range []string{"low", "high"} {
		t.Run(effort, func(t *testing.T) {
			p := &initialConfigProvider{response: NewSessionResponse{
				SessionID: "session-1", Modes: &SessionModeState{CurrentModeID: "default"},
				ConfigOptions: []SessionConfigOption{configOption("effort", "Effort", "high")},
			}}
			service, peer := p.connect(t)
			p.change = func(id string, _ any) error {
				if id != "mode" {
					return nil
				}
				p.response.ConfigOptions[0].Value = effort
				return peer.Notify(context.Background(), "session/update", SessionNotification{SessionID: "session-1", Update: SessionUpdate{SessionUpdate: "config_option_update", ConfigOptions: p.response.ConfigOptions}})
			}
			driver, err := service.Create(context.Background(), StartSessionOptions{InitialConfig: InitialConfig{Mode: "plan", Effort: "high"}})
			if err != nil {
				t.Fatal(err)
			}
			p.mu.Lock()
			if want := []string{"mode=plan", "effort=high"}; !reflect.DeepEqual(p.calls, want) {
				t.Errorf("calls = %v, want %v", p.calls, want)
			}
			p.mu.Unlock()
			acpDriver := driver.(*acpSessionDriver)
			if !initialConfigOptionEqual(acpDriver, "effort", "high") {
				t.Fatal("full effort response was not authoritative")
			}
			acpDriver.handleSessionUpdate(SessionNotification{Update: SessionUpdate{SessionUpdate: "current_mode_update", CurrentModeID: "default"}})
			if initialConfigOptionEqual(acpDriver, "effort", "high") {
				t.Fatal("unsolicited mode change left old effort snapshot trusted")
			}
		})
	}
}

func TestInitialConfigConcurrentConfigUpdates(t *testing.T) {
	driver := configOptionTestDriver([]SessionConfigOption{configOption("model", "Model", "haiku")})
	connectConfigOptionProvider(t, driver, func(SetSessionConfigOptionRequest) SetSessionConfigOptionResponse {
		return SetSessionConfigOptionResponse{}
	})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			driver.handleSessionUpdate(SessionNotification{Update: SessionUpdate{SessionUpdate: "config_option_update", ConfigOptions: []SessionConfigOption{configOption("model", "Model", "haiku")}}})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			if err := driver.SetAgentConfigOption(context.Background(), "model", "haiku"); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	for i := 0; i < 100; i++ {
		if _, err := applyInitialConfig(context.Background(), driver, InitialConfig{Model: "haiku"}, defaultAgentProfile()); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
}

func configOption(id, name string, value any) SessionConfigOption {
	return SessionConfigOption{
		Type:  "select",
		ID:    id,
		Name:  name,
		Value: value,
		Options: []SessionConfigChoice{
			{Value: value, Name: name + " value"},
		},
	}
}

func configOptionTestDriver(options []SessionConfigOption) *acpSessionDriver {
	driver := &acpSessionDriver{
		sessionID: "session-1",
		metadata: RuntimeSessionMetadata{
			SessionID: "session-1",
		},
		rawConfig: map[string]any{},
	}
	driver.replaceConfigOptionsLocked(options)
	return driver
}

func connectConfigOptionProvider(
	t *testing.T,
	driver *acpSessionDriver,
	handler func(SetSessionConfigOptionRequest) SetSessionConfigOptionResponse,
) *Peer {
	t.Helper()
	providerReader, runtimeWriter := io.Pipe()
	runtimeReader, providerWriter := io.Pipe()
	runtimePeer := NewPeer(runtimeReader, runtimeWriter, PeerOptions{})
	providerPeer := NewPeer(providerReader, providerWriter, PeerOptions{})
	providerPeer.RegisterRequest("session/set_config_option", func(_ context.Context, raw json.RawMessage) (any, error) {
		var req SetSessionConfigOptionRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, err
		}
		return handler(req), nil
	})
	driver.connection = NewConnection(runtimePeer, Client{})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	go func() { _ = runtimePeer.Start(ctx) }()
	go func() { _ = providerPeer.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		runtimePeer.Close()
		providerPeer.Close()
		_ = providerReader.Close()
		_ = runtimeWriter.Close()
		_ = runtimeReader.Close()
		_ = providerWriter.Close()
	})
	return providerPeer
}
