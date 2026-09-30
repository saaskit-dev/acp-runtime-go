package openai

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	acp "github.com/saaskit-dev/acp-runtime-go"
)

type gatewayPromptFixture func(context.Context, *acp.Peer, string) error

// This fixture exercises the actual HTTP -> Runtime -> ACP wire -> streaming
// path. It neither shells out to a provider nor reads real credentials.
func gatewayFixture(t *testing.T, prompt gatewayPromptFixture, configure func(*Config)) (*Server, *httptest.Server) {
	t.Helper()
	factory := func(ctx context.Context, input acp.ConnectionFactoryInput) (acp.ConnectionHandle, error) {
		clientIn, serverOut := io.Pipe()
		serverIn, clientOut := io.Pipe()
		provider := acp.NewPeer(serverIn, serverOut, acp.PeerOptions{})
		client := acp.NewPeer(clientIn, clientOut, acp.PeerOptions{})
		provider.RegisterRequest("initialize", func(context.Context, json.RawMessage) (any, error) {
			return map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{}, "authMethods": []any{}}, nil
		})
		provider.RegisterRequest("session/new", func(context.Context, json.RawMessage) (any, error) {
			return map[string]any{"sessionId": "fixture-session"}, nil
		})
		provider.RegisterRequest("session/prompt", func(ctx context.Context, raw json.RawMessage) (any, error) {
			var req struct {
				SessionID string `json:"sessionId"`
			}
			if err := json.Unmarshal(raw, &req); err != nil {
				return nil, err
			}
			if err := prompt(ctx, provider, req.SessionID); err != nil {
				return nil, err
			}
			return map[string]any{"stopReason": "end_turn"}, nil
		})
		runCtx, cancel := context.WithCancel(context.Background())
		go provider.Start(runCtx)
		go client.Start(runCtx)
		return acp.ConnectionHandle{Connection: acp.NewConnection(client, input.Client), Dispose: func(context.Context) error {
			cancel()
			client.Close()
			provider.Close()
			_ = clientIn.Close()
			_ = serverOut.Close()
			_ = serverIn.Close()
			_ = clientOut.Close()
			return nil
		}}, nil
	}
	config := Config{ConnectionFactory: factory, DefaultAgentID: "fixture", CWD: t.TempDir(), ResolveAgent: func(context.Context, string) (acp.Agent, error) {
		return acp.Agent{Type: "fixture", Command: "in-memory-fixture"}, nil
	}}
	if configure != nil {
		configure(&config)
	}
	app := NewServer(config)
	server := httptest.NewServer(app.Handler())
	t.Cleanup(func() { server.Close(); _ = app.Close(context.Background()) })
	return app, server
}

func fixtureText(ctx context.Context, peer *acp.Peer, id, text string) error {
	return peer.Notify(ctx, "session/update", map[string]any{"sessionId": id, "update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": text}}})
}

func TestGatewayBusyHTTPStatus(t *testing.T) {
	for _, endpoint := range []string{"/v1/chat/completions", "/v1/responses"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", endpoint, stream), func(t *testing.T) {
				app := &Server{ctx: context.Background(), defaultAgentID: "fixture", cwd: "/tmp", sessionTTL: time.Hour, sessions: map[string]*sessionRecord{}, responses: map[string]string{}}
				request := httptest.NewRequest(http.MethodPost, endpoint, nil)
				var rc requestContext
				body := fmt.Sprintf(`{"stream":%t,"messages":[{"role":"user","content":"hello"}]}`, stream)
				if endpoint == "/v1/responses" {
					rc = app.buildResponseRequestContext(request, responseRequest{})
					body = fmt.Sprintf(`{"stream":%t,"input":"hello"}`, stream)
				} else {
					rc = app.buildRequestContext(request, chatCompletionRequest{})
				}
				app.sessions["busy"] = &sessionRecord{id: "busy", ownerHash: rc.ownerHash, fingerprint: rc.fingerprint, busy: true, expiresAt: time.Now().Add(-time.Hour)}
				server := httptest.NewServer(app.Handler())
				defer server.Close()
				response := postJSON(t, server, endpoint, body, map[string]string{headerSessionID: "busy"})
				defer response.Body.Close()
				data, _ := io.ReadAll(response.Body)
				if response.StatusCode != http.StatusConflict || !strings.Contains(string(data), `"code":"session_busy"`) {
					t.Fatalf("status=%d body=%s", response.StatusCode, data)
				}
				if record, ok := app.getSession("busy"); !ok || record.closed {
					t.Fatal("busy expired session removed")
				}
				if strings.Contains(response.Header.Get("Content-Type"), "event-stream") {
					t.Fatal("busy error committed SSE headers")
				}
			})
		}
	}
}

func TestGatewayTTLConcurrentAccess(t *testing.T) {
	record := &sessionRecord{id: "session", ownerHash: "owner", fingerprint: "fp", expiresAt: time.Now().Add(time.Hour)}
	app := &Server{sessions: map[string]*sessionRecord{record.id: record}, responses: map[string]string{}}
	rc := requestContext{ownerHash: record.ownerHash, fingerprint: record.fingerprint}
	var wg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 2000; i++ {
				switch worker {
				case 0:
					record.tryBegin(time.Hour)
					record.end(false, time.Hour)
				case 1:
					_, _ = app.validatePersistentSession(record.id, rc)
				case 2:
					app.cleanupExpired()
				case 3:
					app.sessionStats()
				}
			}
		}(worker)
	}
	wg.Wait()
	if _, ok := app.getSession(record.id); !ok {
		t.Fatal("fresh session removed")
	}
}

func TestGatewayIdentitySafeDeletionAndLease(t *testing.T) {
	old := &sessionRecord{id: "same", expiresAt: time.Now().Add(-time.Hour)}
	current := &sessionRecord{id: "same", expiresAt: time.Now().Add(time.Hour)}
	app := &Server{sessions: map[string]*sessionRecord{"same": current}, responses: map[string]string{"new-response": "same"}}
	if app.removeSession(old) {
		t.Fatal("stale record deleted replacement")
	}
	if err := app.beginSession(old); err == nil {
		t.Fatal("stale record acquired lease")
	}
	if got, ok := app.getSession("same"); !ok || got != current {
		t.Fatal("replacement lost")
	}
	if _, ok := app.sessionIDForResponse("new-response"); !ok {
		t.Fatal("replacement aliases removed")
	}
	if err := app.beginSession(current); err != nil {
		t.Fatal(err)
	}
	current.mu.Lock()
	current.expiresAt = time.Now().Add(-time.Hour)
	current.mu.Unlock()
	app.cleanupExpired()
	if _, ok := app.getSession("same"); !ok {
		t.Fatal("cleanup removed active lease")
	}
	current.end(false, time.Hour)
	current.mu.Lock()
	expiry := current.expiresAt
	current.mu.Unlock()
	if !expiry.After(time.Now().Add(59 * time.Minute)) {
		t.Fatal("idle TTL not restarted at turn end")
	}
	if !app.removeSession(current) {
		t.Fatal("could not remove current record")
	}
	if current.tryBegin(time.Hour) {
		t.Fatal("deleted record reused")
	}
}

func TestGatewayRejectsUnsupportedSemantics(t *testing.T) {
	var starts atomic.Int32
	app, server := gatewayFixture(t, func(ctx context.Context, peer *acp.Peer, id string) error { return fixtureText(ctx, peer, id, "OK") }, func(config *Config) {
		base := config.ResolveAgent
		config.ResolveAgent = func(ctx context.Context, id string) (acp.Agent, error) { starts.Add(1); return base(ctx, id) }
	})
	_ = app
	cases := []struct{ endpoint, fields, param string }{
		{"chat/completions", `"max_tokens":1,`, "max_tokens"},
		{"chat/completions", `"max_completion_tokens":0,`, "max_completion_tokens"},
		{"chat/completions", `"temperature":0,`, "temperature"},
		{"chat/completions", `"top_p":0.5,`, "top_p"},
		{"chat/completions", `"stop":["END"],`, "stop"},
		{"responses", `"max_output_tokens":10,`, "max_output_tokens"},
		{"responses", `"temperature":2,`, "temperature"},
		{"responses", `"top_p":0.8,`, "top_p"},
		{"responses", `"stop":"END",`, "stop"},
		{"responses", `"reasoning":{"summary":"auto"},`, "reasoning.summary"},
	}
	for _, tc := range cases {
		t.Run(tc.endpoint+"/"+tc.param, func(t *testing.T) {
			input := `"messages":[{"role":"user","content":"hi"}]`
			if tc.endpoint == "responses" {
				input = `"input":"hi"`
			}
			response := postJSON(t, server, "/v1/"+tc.endpoint, "{"+tc.fields+input+"}", nil)
			defer response.Body.Close()
			var body openAIErrorResponse
			if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != 400 || body.Error.Param != tc.param {
				t.Fatalf("status=%d error=%+v", response.StatusCode, body.Error)
			}
		})
	}
	for _, tc := range []struct{ endpoint, body string }{
		{"chat/completions", `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.invalid/image.png"}}]}]}`},
		{"responses", `{"input":[{"role":"user","content":[{"type":"input_image","image_url":"https://example.invalid/image.png"}]}]}`},
	} {
		response := postJSON(t, server, "/v1/"+tc.endpoint, tc.body, nil)
		response.Body.Close()
		if response.StatusCode != 400 {
			t.Fatalf("image status=%d", response.StatusCode)
		}
	}
	if starts.Load() != 0 {
		t.Fatalf("unsupported input started provider %d times", starts.Load())
	}
}

func TestGatewayConcurrentStartBudget(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	var starts atomic.Int32
	app, server := gatewayFixture(t, func(ctx context.Context, peer *acp.Peer, id string) error { return fixtureText(ctx, peer, id, "OK") }, func(config *Config) {
		config.MaxConcurrentSessions = 1
		base := config.ConnectionFactory
		config.ConnectionFactory = func(ctx context.Context, input acp.ConnectionFactoryInput) (acp.ConnectionHandle, error) {
			starts.Add(1)
			close(entered)
			select {
			case <-release:
				return base(ctx, input)
			case <-ctx.Done():
				return acp.ConnectionHandle{}, ctx.Err()
			}
		}
	})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		resp, err := server.Client().Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"messages":[{"role":"user","content":"hi"}]}`))
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("start not entered")
	}
	for _, endpoint := range []string{"chat/completions", "responses"} {
		body := `{"messages":[{"role":"user","content":"hi"}]}`
		if endpoint == "responses" {
			body = `{"input":"hi"}`
		}
		response := postJSON(t, server, "/v1/"+endpoint, body, nil)
		data, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != 429 {
			t.Fatalf("status=%d body=%s", response.StatusCode, data)
		}
	}
	if _, err := app.discoverAgentModels(context.Background(), "fixture"); !isSessionError(err, "session_limit") {
		t.Fatalf("discovery admission=%v", err)
	}
	close(release)
	wg.Wait()
	if starts.Load() != 1 {
		t.Fatalf("factory starts=%d", starts.Load())
	}
	app.mu.Lock()
	live, pending := len(app.liveSessions), app.pendingStarts
	app.mu.Unlock()
	if live != 0 || pending != 0 {
		t.Fatalf("temporary capacity leaked: live=%d pending=%d", live, pending)
	}
}

// Delay the first wire write until the provider burst has terminalized. This
// forces real Turn.Events drops without blocking the provider read loop.
type pausedSSEWriter struct {
	*httptest.ResponseRecorder
	once   sync.Once
	resume <-chan struct{}
}

func (w *pausedSSEWriter) Write(data []byte) (int, error) {
	w.once.Do(func() {
		select {
		case <-w.resume:
		case <-time.After(5 * time.Second):
		}
	})
	return w.ResponseRecorder.Write(data)
}

func TestGatewaySlowSSERecoversCompleteText(t *testing.T) {
	for _, endpoint := range []string{"chat/completions", "responses"} {
		t.Run(endpoint, func(t *testing.T) {
			terminal := make(chan struct{})
			var drops atomic.Int32
			var want strings.Builder
			for i := 0; i < 2048; i++ {
				fmt.Fprintf(&want, "%04d字;", i)
			}
			app, _ := gatewayFixture(t, func(ctx context.Context, peer *acp.Peer, id string) error {
				for i := 0; i < 2048; i++ {
					if err := fixtureText(ctx, peer, id, fmt.Sprintf("%04d字;", i)); err != nil {
						return err
					}
				}
				return nil
			}, func(config *Config) {
				config.RuntimeOptions.Hooks.OnEventDrop = func(acp.RuntimeEventDrop) { drops.Add(1) }
				config.RuntimeOptions.Hooks.OnTurnEvent = func(event acp.RuntimeTurnEvent) {
					if event.Type == "completed" {
						close(terminal)
					}
				}
			})
			body := `{"stream":true,"messages":[{"role":"user","content":"burst"}]}`
			if endpoint == "responses" {
				body = `{"stream":true,"store":false,"input":"burst"}`
			}
			writer := &pausedSSEWriter{ResponseRecorder: httptest.NewRecorder(), resume: terminal}
			app.Handler().ServeHTTP(writer, httptest.NewRequest(http.MethodPost, "/v1/"+endpoint, strings.NewReader(body)))
			got, failures, done := sseText(t, writer.Body.String(), endpoint)
			if drops.Load() == 0 {
				t.Fatal("fixture did not exercise lossy preview")
			}
			if failures != 0 || !done || got != want.String() {
				t.Fatalf("status=%d bytes=%d/%d failures=%d done=%t", writer.Code, len(got), want.Len(), failures, done)
			}
		})
	}
}

func sseText(t *testing.T, data, endpoint string) (string, int, bool) {
	t.Helper()
	var text strings.Builder
	failures := 0
	done := false
	scanner := bufio.NewScanner(strings.NewReader(data))
	scanner.Buffer(make([]byte, 1024), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "data: [DONE]" {
			done = true
			continue
		}
		if !strings.HasPrefix(line, "data: {") {
			continue
		}
		raw := []byte(strings.TrimPrefix(line, "data: "))
		var payload map[string]json.RawMessage
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Fatal(err)
		}
		if _, ok := payload["error"]; ok {
			failures++
			continue
		}
		if endpoint == "chat/completions" {
			var chunk chatCompletionResponse
			if err := json.Unmarshal(raw, &chunk); err != nil {
				t.Fatal(err)
			}
			for _, choice := range chunk.Choices {
				if choice.Delta != nil {
					text.WriteString(choice.Delta.Content)
				}
			}
		} else {
			var event struct {
				Type  string `json:"type"`
				Delta string `json:"delta"`
			}
			json.Unmarshal(raw, &event)
			if event.Type == "response.output_text.delta" {
				text.WriteString(event.Delta)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return text.String(), failures, done
}

func TestGatewayMissingMiddleFailsExplicitly(t *testing.T) {
	events := make(chan acp.TurnEvent, 3)
	events <- acp.TurnEvent{Type: "text", Text: "A"}
	events <- acp.TurnEvent{Type: "text", Text: "C"}
	events <- acp.TurnEvent{Type: "failed", Error: errors.New("preview failure is not terminal")}
	close(events)
	results := make(chan acp.TurnResult, 1)
	results <- acp.TurnResult{Completion: acp.TurnCompletion{OutputText: "ABC", StopReason: "end_turn"}}
	close(results)
	_, err := (&Server{}).consumeText(context.Background(), acp.TurnHandle{Events: events, Completion: results}, nil, func(string) error { return nil })
	if !isSessionError(err, "stream_gap") {
		t.Fatalf("missing middle error=%v", err)
	}
}

func TestGatewayOutputByteLimitHTTP(t *testing.T) {
	_, server := gatewayFixture(t, func(ctx context.Context, peer *acp.Peer, id string) error {
		return fixtureText(ctx, peer, id, "too long")
	}, func(config *Config) { config.MaxOutputBytes = 4 })
	for _, endpoint := range []string{"chat/completions", "responses"} {
		for _, stream := range []bool{false, true} {
			body := fmt.Sprintf(`{"stream":%t,"messages":[{"role":"user","content":"hi"}]}`, stream)
			if endpoint == "responses" {
				body = fmt.Sprintf(`{"stream":%t,"store":false,"input":"hi"}`, stream)
			}
			response := postJSON(t, server, "/v1/"+endpoint, body, nil)
			data, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if !strings.Contains(string(data), `"code":"output_limit"`) || strings.Contains(string(data), "[DONE]") {
				t.Fatalf("limit response=%s", data)
			}
			if !stream && response.StatusCode != 502 {
				t.Fatalf("nonstream limit status=%d", response.StatusCode)
			}
		}
	}
}

func TestGatewayAliasesBoundedAndIdentitySafe(t *testing.T) {
	record := &sessionRecord{id: "session"}
	app := &Server{sessions: map[string]*sessionRecord{record.id: record}, responses: map[string]string{}, maxResponseAliases: 2}
	for _, id := range []string{"r1", "r2", "r3"} {
		app.registerResponseSession(id, record)
	}
	if _, ok := app.sessionIDForResponse("r1"); ok {
		t.Fatal("old alias not evicted")
	}
	app.registerResponseSession("stale", &sessionRecord{id: record.id})
	if _, ok := app.sessionIDForResponse("stale"); ok {
		t.Fatal("stale record registered alias")
	}
	app.removeSession(record)
	if len(app.responses) != 0 || len(app.responseOrder) != 0 {
		t.Fatal("aliases retained after removal")
	}
}

func TestGatewayNativeInitialModelHTTP(t *testing.T) {
	for _, endpoint := range []string{"chat/completions", "responses"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", endpoint, stream), func(t *testing.T) {
				app := NewServer(Config{DefaultAgentID: "codex-native", CWD: t.TempDir(), Models: []string{"selected-http-model"}, ResolveAgent: func(context.Context, string) (acp.Agent, error) {
					return acp.CreateCodexNativeAgent(acp.Agent{Command: os.Args[0], Args: []string{"-test.run=^TestGatewayNativeModelHelper$", "--"}, Env: map[string]string{"ACP_GATEWAY_MODEL_HELPER": "1"}}), nil
				}})
				server := httptest.NewServer(app.Handler())
				defer server.Close()
				defer app.Close(context.Background())
				body := fmt.Sprintf(`{"model":"selected-http-model","stream":%t,"messages":[{"role":"user","content":"hi"}]}`, stream)
				if endpoint == "responses" {
					body = fmt.Sprintf(`{"model":"selected-http-model","store":false,"stream":%t,"input":"hi"}`, stream)
				}
				response := postJSON(t, server, "/v1/"+endpoint, body, nil)
				data, _ := io.ReadAll(response.Body)
				response.Body.Close()
				if response.StatusCode != 200 || !strings.Contains(string(data), "MODEL=selected-http-model") {
					t.Fatalf("status=%d body=%s", response.StatusCode, data)
				}
				if stream {
					_, failures, done := sseText(t, string(data), endpoint)
					if failures != 0 || !done {
						t.Fatalf("stream failures=%d done=%t body=%s", failures, done, data)
					}
				}
			})
		}
	}
}

// A local app-server subprocess proving model choice reaches thread/start before
// any turn. Its deliberately different default prevents a false positive.
func TestGatewayNativeModelHelper(t *testing.T) {
	if os.Getenv("ACP_GATEWAY_MODEL_HELPER") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	write := func(v any) { data, _ := json.Marshal(v); fmt.Fprintln(os.Stdout, string(data)) }
	model := "provider-default-must-not-match"
	for scanner.Scan() {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil || len(request.ID) == 0 {
			continue
		}
		respond := func(result any) { write(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}) }
		switch request.Method {
		case "initialize":
			respond(map[string]any{"userAgent": "gateway-fixture/0.153.4"})
		case "thread/start":
			var params struct {
				Model string `json:"model"`
			}
			json.Unmarshal(request.Params, &params)
			if params.Model != "" {
				model = params.Model
			}
			respond(map[string]any{"thread": map[string]any{"id": "http-thread"}, "model": model})
		case "turn/start":
			respond(map[string]any{"turn": map[string]any{"id": "http-turn", "status": "inProgress"}})
			write(map[string]any{"jsonrpc": "2.0", "method": "item/agentMessage/delta", "params": map[string]any{"threadId": "http-thread", "turnId": "http-turn", "itemId": "http-message", "delta": "MODEL=" + model}})
			write(map[string]any{"jsonrpc": "2.0", "method": "turn/completed", "params": map[string]any{"threadId": "http-thread", "turnId": "http-turn", "turn": map[string]any{"id": "http-turn", "status": "completed"}}})
		default:
			write(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32601, "message": "unsupported fixture method"}})
		}
	}
}

func TestGatewayLongTurnIdleTTLHTTP(t *testing.T) {
	for _, endpoint := range []string{"chat/completions", "responses"} {
		t.Run(endpoint, func(t *testing.T) {
			release := make(chan struct{})
			t.Cleanup(func() {
				select {
				case <-release:
				default:
					close(release)
				}
			})
			remoteCancelled := make(chan struct{}, 1)
			app, server := gatewayFixture(t, func(ctx context.Context, peer *acp.Peer, id string) error {
				if err := fixtureText(ctx, peer, id, "LONG"); err != nil {
					return err
				}
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					remoteCancelled <- struct{}{}
					return ctx.Err()
				}
			}, nil)
			body := `{"stream":true,"messages":[{"role":"user","content":"hi"}]}`
			if endpoint == "responses" {
				body = `{"stream":true,"input":"hi"}`
			}
			response := postJSON(t, server, "/v1/"+endpoint, body, map[string]string{headerSessionMode: "persistent"})
			defer response.Body.Close()
			id := response.Header.Get(headerSessionID)
			record, ok := app.getSession(id)
			if !ok {
				t.Fatal("missing live session")
			}
			record.mu.Lock()
			record.expiresAt = time.Now().Add(-time.Hour)
			record.mu.Unlock()
			busy := postJSON(t, server, "/v1/"+endpoint, body, map[string]string{headerSessionID: id})
			data, _ := io.ReadAll(busy.Body)
			busy.Body.Close()
			if busy.StatusCode != 409 || !strings.Contains(string(data), "session_busy") {
				t.Fatalf("busy status=%d body=%s", busy.StatusCode, data)
			}
			app.cleanupExpired()
			if _, ok := app.getSession(id); !ok {
				t.Fatal("active session removed")
			}
			select {
			case <-remoteCancelled:
				t.Fatal("busy request cancelled active provider")
			default:
			}
			close(release)
			data, _ = io.ReadAll(response.Body)
			if !strings.Contains(string(data), "[DONE]") {
				t.Fatalf("original request failed: %s", data)
			}
			deadline := time.Now().Add(time.Second)
			for record.isBusy() && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			record.mu.Lock()
			expiry, busyState := record.expiresAt, record.busy
			record.mu.Unlock()
			if busyState || !expiry.After(time.Now().Add(29*time.Minute)) {
				t.Fatalf("idle TTL not renewed: busy=%t expires=%v", busyState, expiry)
			}
		})
	}
}

func TestGatewayDisconnectQuarantinesPersistentSession(t *testing.T) {
	for _, endpoint := range []string{"chat/completions", "responses"} {
		t.Run(endpoint, func(t *testing.T) {
			app, server := gatewayFixture(t, func(ctx context.Context, peer *acp.Peer, id string) error {
				if err := fixtureText(ctx, peer, id, "prefix"); err != nil {
					return err
				}
				<-ctx.Done()
				return ctx.Err()
			}, nil)
			body := `{"stream":true,"messages":[{"role":"user","content":"hi"}]}`
			if endpoint == "responses" {
				body = `{"stream":true,"input":"hi"}`
			}
			response := postJSON(t, server, "/v1/"+endpoint, body, map[string]string{headerSessionMode: "persistent"})
			id := response.Header.Get(headerSessionID)
			response.Body.Close()
			record, ok := app.getSession(id)
			if !ok {
				t.Fatal("missing session")
			}
			deadline := time.Now().Add(3 * time.Second)
			for {
				record.mu.Lock()
				tainted, busy := record.tainted, record.busy
				record.mu.Unlock()
				if tainted && !busy {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("disconnected turn was not quarantined")
				}
				time.Sleep(time.Millisecond)
			}
			next := postJSON(t, server, "/v1/"+endpoint, body, map[string]string{headerSessionID: id})
			data, _ := io.ReadAll(next.Body)
			next.Body.Close()
			if next.StatusCode != 409 || !strings.Contains(string(data), `"code":"session_tainted"`) {
				t.Fatalf("next status=%d body=%s", next.StatusCode, data)
			}
		})
	}
}

func TestGatewayErrorClassificationPreservesStatuses(t *testing.T) {
	app := &Server{}
	for _, status := range []int{401, 403, 404, 409, 410, 429, 500, 502, 503} {
		original := sessionHTTPError{status: status, code: "retained", message: "original"}
		for _, err := range []error{original, &original, fmt.Errorf("wrapped: %w", original), fmt.Errorf("wrapped: %w", &original)} {
			w := httptest.NewRecorder()
			app.writeTurnError(w, err)
			if w.Code != status || !strings.Contains(w.Body.String(), `"code":"retained"`) {
				t.Fatalf("status=%d want=%d body=%s", w.Code, status, w.Body.String())
			}
		}
	}
	for _, kind := range []acp.ErrorKind{acp.ErrorTurnCoalesced, acp.ErrorSessionClosed} {
		w := httptest.NewRecorder()
		app.writeTurnError(w, &acp.RuntimeError{Kind: kind, Msg: "unavailable"})
		if w.Code != 409 {
			t.Fatalf("kind=%s status=%d", kind, w.Code)
		}
	}
}

func TestGatewayDeleteDoesNotHoldGlobalLockDuringClose(t *testing.T) {
	closing := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	var once sync.Once
	app, server := gatewayFixture(t, func(ctx context.Context, peer *acp.Peer, id string) error { return fixtureText(ctx, peer, id, "OK") }, func(config *Config) {
		base := config.ConnectionFactory
		config.ConnectionFactory = func(ctx context.Context, input acp.ConnectionFactoryInput) (acp.ConnectionHandle, error) {
			handle, err := base(ctx, input)
			dispose := handle.Dispose
			handle.Dispose = func(ctx context.Context) error { once.Do(func() { close(closing); <-release }); return dispose(ctx) }
			return handle, err
		}
	})
	response := postJSON(t, server, "/v1/chat/completions", `{"messages":[{"role":"user","content":"hi"}]}`, map[string]string{headerSessionMode: "persistent"})
	id := response.Header.Get(headerSessionID)
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		request, _ := http.NewRequest(http.MethodDelete, server.URL+"/v1/acp/sessions/"+id, nil)
		response, err := server.Client().Do(request)
		if err == nil {
			response.Body.Close()
		}
	}()
	select {
	case <-closing:
	case <-time.After(time.Second):
		t.Fatal("delete did not reach cleanup")
	}
	checked := make(chan struct{})
	go func() { app.sessionStats(); close(checked) }()
	select {
	case <-checked:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("slow close held global lock")
	}
	close(release)
	<-done
}

func TestGatewayPersistentStartReservation(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	app, server := gatewayFixture(t, func(ctx context.Context, peer *acp.Peer, id string) error { return fixtureText(ctx, peer, id, "OK") }, func(config *Config) {
		config.MaxSessions = 1
		config.MaxConcurrentSessions = 3
		base := config.ConnectionFactory
		config.ConnectionFactory = func(ctx context.Context, input acp.ConnectionFactoryInput) (acp.ConnectionHandle, error) {
			close(entered)
			select {
			case <-release:
				return base(ctx, input)
			case <-ctx.Done():
				return acp.ConnectionHandle{}, ctx.Err()
			}
		}
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		request, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/responses", strings.NewReader(`{"input":"hi"}`))
		response, err := server.Client().Do(request)
		if err == nil {
			io.Copy(io.Discard, response.Body)
			response.Body.Close()
		}
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("start not entered")
	}
	response := postJSON(t, server, "/v1/responses", `{"input":"hi"}`, nil)
	data, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 429 {
		t.Fatalf("persistent reservation status=%d body=%s", response.StatusCode, data)
	}
	close(release)
	<-done
	app.mu.Lock()
	pending, sessions := app.pendingPersistent, len(app.sessions)
	app.mu.Unlock()
	if pending != 0 || sessions != 1 {
		t.Fatalf("pending=%d sessions=%d", pending, sessions)
	}
}

func TestGatewayFailedCleanupRetainsAdmission(t *testing.T) {
	var allowClose atomic.Bool
	app, server := gatewayFixture(t, func(ctx context.Context, peer *acp.Peer, id string) error { return fixtureText(ctx, peer, id, "OK") }, func(config *Config) {
		config.MaxConcurrentSessions = 1
		base := config.ConnectionFactory
		config.ConnectionFactory = func(ctx context.Context, input acp.ConnectionFactoryInput) (acp.ConnectionHandle, error) {
			handle, err := base(ctx, input)
			dispose := handle.Dispose
			handle.Dispose = func(ctx context.Context) error {
				if !allowClose.Load() {
					return errors.New("cleanup still pending")
				}
				return dispose(ctx)
			}
			return handle, err
		}
	})
	t.Cleanup(func() { allowClose.Store(true) })
	response := postJSON(t, server, "/v1/chat/completions", `{"messages":[{"role":"user","content":"hi"}]}`, nil)
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	response = postJSON(t, server, "/v1/chat/completions", `{"messages":[{"role":"user","content":"hi"}]}`, nil)
	data, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 429 {
		t.Fatalf("pending cleanup lost admission ownership: status=%d body=%s", response.StatusCode, data)
	}
	if err := app.Close(context.Background()); err == nil {
		t.Fatal("failed close reported success")
	}
	allowClose.Store(true)
	if err := app.Close(context.Background()); err != nil {
		t.Fatalf("retry close: %v", err)
	}
	app.mu.Lock()
	live := len(app.liveSessions)
	app.mu.Unlock()
	if live != 0 {
		t.Fatal("cleanup reservation leaked")
	}
}

type failedFlushWriter struct{ *httptest.ResponseRecorder }

func (w failedFlushWriter) FlushError() error { return errors.New("client disconnected during flush") }

func TestGatewayFlushErrorsReachCancellation(t *testing.T) {
	app, _ := gatewayFixture(t, func(ctx context.Context, peer *acp.Peer, id string) error { <-ctx.Done(); return ctx.Err() }, func(config *Config) { config.AccessLog = func(AccessLogEntry) {} })
	for _, endpoint := range []string{"chat/completions", "responses"} {
		body := `{"stream":true,"messages":[{"role":"user","content":"hi"}]}`
		if endpoint == "responses" {
			body = `{"stream":true,"input":"hi"}`
		}
		request := httptest.NewRequest(http.MethodPost, "/v1/"+endpoint, strings.NewReader(body))
		request.Header.Set(headerSessionMode, "persistent")
		writer := failedFlushWriter{httptest.NewRecorder()}
		app.Handler().ServeHTTP(writer, request)
		record, ok := app.getSession(writer.Header().Get(headerSessionID))
		if !ok {
			t.Fatal("missing persistent record")
		}
		record.mu.Lock()
		tainted, busy := record.tainted, record.busy
		record.mu.Unlock()
		if !tainted || busy {
			t.Fatalf("failed flush did not revoke lease: tainted=%t busy=%t", tainted, busy)
		}
		if strings.Contains(writer.Body.String(), "[DONE]") {
			t.Fatal("failed flush reported success")
		}
	}
}

type deadlineWriter struct {
	*httptest.ResponseRecorder
	deadlines []time.Time
}

func (w *deadlineWriter) SetWriteDeadline(deadline time.Time) error {
	w.deadlines = append(w.deadlines, deadline)
	return nil
}
func TestGatewayStreamDeadlineDoesNotLeak(t *testing.T) {
	app, _ := gatewayFixture(t, func(ctx context.Context, peer *acp.Peer, id string) error { return fixtureText(ctx, peer, id, "OK") }, nil)
	for _, endpoint := range []string{"chat/completions", "responses"} {
		body := `{"stream":true,"messages":[{"role":"user","content":"hi"}]}`
		if endpoint == "responses" {
			body = `{"stream":true,"store":false,"input":"hi"}`
		}
		writer := &deadlineWriter{ResponseRecorder: httptest.NewRecorder()}
		app.Handler().ServeHTTP(writer, httptest.NewRequest(http.MethodPost, "/v1/"+endpoint, strings.NewReader(body)))
		if len(writer.deadlines) < 2 || writer.deadlines[0].IsZero() || !writer.deadlines[len(writer.deadlines)-1].IsZero() {
			t.Fatalf("SSE deadlines were not applied and cleared: %v", writer.deadlines)
		}
	}
}
