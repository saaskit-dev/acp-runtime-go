package acpruntime

import (
	"context"
	"encoding/json"
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
) {
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
}
