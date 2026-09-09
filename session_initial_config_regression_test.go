package acpruntime

import (
	"bytes"
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

func TestInitialConfigNotificationFencesOldResponse(t *testing.T) {
	for _, path := range []string{"create", "resume"} {
		for _, notification := range []string{"current_mode_update", "config_option_update"} {
			t.Run(path+"/"+notification, func(t *testing.T) {
				p := &initialConfigProvider{response: NewSessionResponse{
					SessionID: "session-1", Modes: &SessionModeState{CurrentModeID: "default"},
					ConfigOptions: []SessionConfigOption{configOption("model", "Model", "haiku"), configOption("effort", "Effort", "high")},
				}}
				service, peer := p.connect(t)
				peer.RegisterRequest("session/set_config_option", func(ctx context.Context, raw json.RawMessage) (any, error) {
					var req SetSessionConfigOptionRequest
					if err := json.Unmarshal(raw, &req); err != nil {
						return nil, err
					}
					p.mu.Lock()
					defer p.mu.Unlock()
					p.calls = append(p.calls, fmt.Sprintf("%s=%v", req.OptionID, req.Value))
					if req.OptionID == "model" {
						p.response.ConfigOptions[0].Value = req.Value
						beforeChange := append([]SessionConfigOption(nil), p.response.ConfigOptions...)
						p.response.Modes = &SessionModeState{CurrentModeID: "plan"}
						p.response.ConfigOptions[1].Value = "low"
						update := SessionUpdate{SessionUpdate: notification, CurrentModeID: "plan", ConfigOptions: p.response.ConfigOptions}
						if err := peer.Notify(ctx, "session/update", SessionNotification{SessionID: "session-1", Update: update}); err != nil {
							return nil, err
						}
						return SetSessionConfigOptionResponse{ConfigOptions: &beforeChange}, nil
					}
					p.response.ConfigOptions[1].Value = req.Value
					current := append([]SessionConfigOption(nil), p.response.ConfigOptions...)
					return SetSessionConfigOptionResponse{ConfigOptions: &current}, nil
				})
				input := StartSessionOptions{InitialConfig: InitialConfig{Model: "sonnet", Effort: "high"}}
				var err error
				if path == "create" {
					_, err = service.Create(context.Background(), input)
				} else {
					_, err = service.Resume(context.Background(), ResumeSessionOptions{StartSessionOptions: input, SessionID: "session-1"})
				}
				if err != nil {
					t.Fatal(err)
				}
				p.mu.Lock()
				defer p.mu.Unlock()
				want := []string{"model=sonnet", "effort=high"}
				if !reflect.DeepEqual(p.calls, want) || p.response.ConfigOptions[1].Value != "high" {
					t.Fatalf("calls=%v provider effort=%v; want %v and provider effort=high", p.calls, p.response.ConfigOptions[1].Value, want)
				}
			})
		}
	}
}

// A later notification follows the response on the wire. Padding delays Call's
// decode so the read loop can apply that notification first, without sleeps.
type initialConfigFollowupWriter struct {
	writer   io.Writer
	followup []byte
}

func (w initialConfigFollowupWriter) Write(data []byte) (int, error) {
	if bytes.Contains(data, []byte(`"configOptions"`)) && bytes.Contains(data, []byte(`"padding"`)) {
		both := append(append([]byte(nil), data...), w.followup...)
		n, err := w.writer.Write(both)
		if n > len(data) {
			n = len(data)
		}
		return n, err
	}
	return w.writer.Write(data)
}

func TestInitialConfigNewerWireNotificationOvertakesResponse(t *testing.T) {
	for _, path := range []string{"create", "resume"} {
		t.Run(path, func(t *testing.T) {
			p := &initialConfigProvider{response: NewSessionResponse{
				SessionID: "session-1", Modes: &SessionModeState{CurrentModeID: "default"},
				ConfigOptions: []SessionConfigOption{configOption("model", "Model", "haiku"), configOption("effort", "Effort", "high")},
			}}
			service, peer := p.connect(t)
			followup := []byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"config_option_update","configOptions":[{"id":"model","type":"select","currentValue":"sonnet"},{"id":"effort","type":"select","currentValue":"low"}]}}}` + "\n")
			peer.writer = initialConfigFollowupWriter{writer: peer.writer, followup: followup}
			peer.RegisterRequest("session/set_config_option", func(_ context.Context, raw json.RawMessage) (any, error) {
				var req SetSessionConfigOptionRequest
				if err := json.Unmarshal(raw, &req); err != nil {
					return nil, err
				}
				p.mu.Lock()
				defer p.mu.Unlock()
				p.calls = append(p.calls, fmt.Sprintf("%s=%v", req.OptionID, req.Value))
				if req.OptionID == "model" {
					p.response.ConfigOptions[0].Value = req.Value
					beforeChange := append([]SessionConfigOption(nil), p.response.ConfigOptions...)
					for i := 0; i < 25000; i++ {
						beforeChange = append(beforeChange, configOption(fmt.Sprintf("pad-%d", i), "padding", "padding"))
					}
					p.response.ConfigOptions[1].Value = "low"
					return SetSessionConfigOptionResponse{ConfigOptions: &beforeChange}, nil
				}
				p.response.ConfigOptions[1].Value = req.Value
				current := append([]SessionConfigOption(nil), p.response.ConfigOptions...)
				return SetSessionConfigOptionResponse{ConfigOptions: &current}, nil
			})
			input := StartSessionOptions{InitialConfig: InitialConfig{Model: "sonnet", Effort: "high"}}
			var err error
			if path == "create" {
				_, err = service.Create(context.Background(), input)
			} else {
				_, err = service.Resume(context.Background(), ResumeSessionOptions{StartSessionOptions: input, SessionID: "session-1"})
			}
			if err != nil {
				t.Fatal(err)
			}
			p.mu.Lock()
			defer p.mu.Unlock()
			if p.response.ConfigOptions[1].Value != "high" {
				t.Fatalf("wire response then newer notification: calls=%v provider effort=%v, want effort=high RPC", p.calls, p.response.ConfigOptions[1].Value)
			}
		})
	}
}

// Request start/response order is not provider mutation/snapshot capture order.
func TestInitialConfigOverlappingResponseNeedsFreshReadback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	driver := configOptionTestDriver([]SessionConfigOption{configOption("effort", "Effort", "high")})
	oldStarted, newerSnapshotCaptured, releaseNewer := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	var calls []string
	providerEffort := "high"
	connectConfigOptionProvider(t, driver, func(req SetSessionConfigOptionRequest) SetSessionConfigOptionResponse {
		mu.Lock()
		calls = append(calls, fmt.Sprintf("%s=%v", req.OptionID, req.Value))
		mu.Unlock()
		switch req.Value {
		case "old":
			close(oldStarted)
			select {
			case <-newerSnapshotCaptured:
			case <-ctx.Done():
				return SetSessionConfigOptionResponse{}
			}
			mu.Lock()
			providerEffort = "low"
			mu.Unlock()
			options := []SessionConfigOption{configOption("effort", "Effort", "low")}
			return SetSessionConfigOptionResponse{ConfigOptions: &options}
		case "new":
			options := []SessionConfigOption{configOption("effort", "Effort", "high")}
			close(newerSnapshotCaptured)
			select {
			case <-releaseNewer:
			case <-ctx.Done():
				return SetSessionConfigOptionResponse{}
			}
			return SetSessionConfigOptionResponse{ConfigOptions: &options}
		default:
			mu.Lock()
			providerEffort = req.Value.(string)
			mu.Unlock()
			options := []SessionConfigOption{configOption("effort", "Effort", req.Value)}
			return SetSessionConfigOptionResponse{ConfigOptions: &options}
		}
	})
	oldDone, newDone := make(chan error, 1), make(chan error, 1)
	go func() { oldDone <- driver.SetAgentConfigOption(ctx, "model", "old") }()
	select {
	case <-oldStarted:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	go func() { newDone <- driver.SetAgentConfigOption(ctx, "model", "new") }()
	if err := <-oldDone; err != nil {
		t.Fatal(err)
	}
	close(releaseNewer)
	if err := <-newDone; err != nil {
		t.Fatal(err)
	}
	if _, err := applyInitialConfig(ctx, driver, InitialConfig{Effort: "high"}, defaultAgentProfile()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if providerEffort != "high" {
		t.Fatalf("overlap re-authorized old snapshot: calls=%v provider effort=%s, want explicit effort=high", calls, providerEffort)
	}
}

func TestInitialConfigFailedAliasPreservesErrorCleanup(t *testing.T) {
	for _, path := range []string{"create", "resume"} {
		t.Run(path, func(t *testing.T) {
			p := &initialConfigProvider{response: NewSessionResponse{SessionID: "session-1", Modes: &SessionModeState{CurrentModeID: "yolo"}}}
			p.change = func(_ string, _ any) error {
				p.response.Modes = &SessionModeState{CurrentModeID: "default"}
				return errors.New("mode operation failed after partial change")
			}
			service, peer := p.connect(t)
			var cleanup []string
			for _, method := range []string{"session/delete", "session/close"} {
				peer.RegisterRequest(method, func(context.Context, json.RawMessage) (any, error) {
					p.mu.Lock()
					defer p.mu.Unlock()
					cleanup = append(cleanup, method)
					return emptyResponse{}, nil
				})
			}
			input := StartSessionOptions{Agent: Agent{Type: ClaudeCodeACPRegistryID}, InitialConfig: InitialConfig{Mode: "yolo"}}
			var err error
			if path == "create" {
				_, err = service.Create(context.Background(), input)
			} else {
				_, err = service.Resume(context.Background(), ResumeSessionOptions{StartSessionOptions: input, SessionID: "session-1"})
			}
			p.mu.Lock()
			defer p.mu.Unlock()
			var runtimeErr *RuntimeError
			wantCleanup, wantStatus := "session/delete", CleanupSucceeded
			if path == "resume" {
				wantCleanup, wantStatus = "session/close", CleanupNotAttempted
			}
			if !errors.As(err, &runtimeErr) || runtimeErr.Kind != ErrorInitialConfig || runtimeErr.CleanupStatus != wantStatus || !reflect.DeepEqual(cleanup, []string{wantCleanup}) {
				t.Fatalf("error=%v calls=%v cleanup=%v; want initial-config error and %s", err, p.calls, cleanup, wantCleanup)
			}
		})
	}
}

func TestInitialConfigFailedModeAliasNeedsReadback(t *testing.T) {
	for _, path := range []string{"create", "resume"} {
		t.Run(path, func(t *testing.T) {
			p := &initialConfigProvider{response: NewSessionResponse{SessionID: "session-1", Modes: &SessionModeState{CurrentModeID: "yolo"}}}
			p.change = func(_ string, value any) error {
				if value == "bypassPermissions" {
					// A failed RPC does not guarantee provider rollback.
					p.response.Modes = &SessionModeState{CurrentModeID: "default"}
					return errors.New("failed after partial mode change")
				}
				return nil
			}
			service, _ := p.connect(t)
			input := StartSessionOptions{Agent: Agent{Type: ClaudeCodeACPRegistryID}, InitialConfig: InitialConfig{Mode: "yolo"}}
			var err error
			if path == "create" {
				_, err = service.Create(context.Background(), input)
			} else {
				_, err = service.Resume(context.Background(), ResumeSessionOptions{StartSessionOptions: input, SessionID: "session-1"})
			}
			if err != nil {
				t.Fatal(err)
			}
			p.mu.Lock()
			defer p.mu.Unlock()
			if want := []string{"mode=bypassPermissions", "mode=yolo"}; !reflect.DeepEqual(p.calls, want) || p.response.Modes.CurrentModeID != "yolo" {
				t.Fatalf("calls=%v provider mode=%s; want fallback RPC and mode=yolo", p.calls, p.response.Modes.CurrentModeID)
			}
		})
	}
}
