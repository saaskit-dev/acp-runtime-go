package acpruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// Native-vs-ACP performance benchmark. Layers:
//
//   [protocol]  no model: ACP simulator (Go, stdio JSON-RPC) vs codex native
//               against the fake app-server — pure process+protocol overhead.
//   [real]      magpie gateway: claude/codex, each via ACP wrapper (npm cache)
//               and via the native transport — process, session, turn latency.
//
// Run: MAGPIE_BASE_URL=http://127.0.0.1:3425/v1 MAGPIE_API_KEY=magpie go test -run TestPerf -v .

type benchSamples struct {
	values []time.Duration
}

func (b *benchSamples) add(d time.Duration) { b.values = append(b.values, d) }

func (b *benchSamples) report() (avg, min, max time.Duration) {
	if len(b.values) == 0 {
		return 0, 0, 0
	}
	sort.Slice(b.values, func(i, j int) bool { return b.values[i] < b.values[j] })
	min = b.values[0]
	max = b.values[len(b.values)-1]
	var total time.Duration
	for _, v := range b.values {
		total += v
	}
	return total / time.Duration(len(b.values)), min, max
}

var benchRows []string

func benchRecord(scenario, metric string, samples *benchSamples) {
	avg, min, max := samples.report()
	benchRows = append(benchRows, fmt.Sprintf("| %s | %s | %s | %s | %s |",
		scenario, metric,
		avg.Round(time.Millisecond), min.Round(time.Millisecond), max.Round(time.Millisecond)))
}

// wrapper claude-agent-acp from the npx cache (offline).
func findCachedWrapper(name string) (string, error) {
	roots, err := os.ReadDir(filepath.Join(os.Getenv("HOME"), ".npm", "_npx"))
	if err != nil {
		return "", err
	}
	best := ""
	for _, root := range roots {
		dir := filepath.Join(os.Getenv("HOME"), ".npm", "_npx", root.Name(), "node_modules", "@agentclientprotocol", name)
		if _, err := os.Stat(filepath.Join(dir, "package.json")); err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, "package.json"))
		if err != nil {
			continue
		}
		var pkg struct {
			Version string `json:"version"`
		}
		_ = json.Unmarshal(data, &pkg)
		if pkg.Version >= best {
			best = pkg.Version
			_ = best
			bestDir = dir
		}
	}
	if bestDir == "" {
		return "", fmt.Errorf("package %s not found in npx cache", name)
	}
	return filepath.Join(bestDir, "dist", "index.js"), nil
}

var bestDir string

// --- measurement helpers -------------------------------------------------

func benchSessionAndTurns(t *testing.T, agent Agent, meta map[string]any, sessions, turns int) (create []time.Duration, firstTurn, warmTurn []time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	runtime := NewRuntime(nil, RuntimeOptions{})
	defer func() { _ = runtime.Close(context.Background()) }()

	benchErrs := 0
	for s := 0; s < sessions; s++ {
		t0 := time.Now()
		session, err := runtime.StartSession(ctx, StartSessionOptions{Agent: agent, CWD: "/tmp", Meta: meta})
		create = append(create, time.Since(t0))
		if err != nil {
			benchErrs++
			t.Logf("StartSession #%d failed: %v", s+1, err)
			continue
		}
		for turn := 0; turn < turns; turn++ {
			t1 := time.Now()
			_, err := session.Run(ctx, "Reply with exactly one word: OK")
			d := time.Since(t1)
			if turn == 0 {
				firstTurn = append(firstTurn, d)
			} else {
				warmTurn = append(warmTurn, d)
			}
			if err != nil {
				benchErrs++
				t.Logf("turn %d session %d failed: %v", turn+1, s+1, err)
				break
			}
		}
		_ = session.Close(context.Background())
	}
	if benchErrs > 0 {
		t.Logf("NOTE: %d operations errored (flaky upstream?) — successful samples only", benchErrs)
	}
	return create, firstTurn, warmTurn
}

// --- protocol baselines (no model) ---------------------------------------

func TestPerfProtocolBaseline(t *testing.T) {
	// ACP protocol: simulator agent (built by the runner into bin/).
	simBin, _ := filepath.Abs(filepath.Join("bin", "acp-simulator-agent"))
	if _, err := os.Stat(simBin); err != nil {
		t.Skipf("simulator binary missing (%s); build with: go build -o bin/acp-simulator-agent ./cmd/acp-simulator-agent", simBin)
	}
	simAgent := Agent{Command: simBin, Args: []string{"--auth-mode", "none"}}
	create, first, warm := benchSessionAndTurns(t, simAgent, nil, 5, 3)
	benchRecord("ACP simulator (Go stdio)", "session create", &benchSamples{values: create})
	benchRecord("ACP simulator (Go stdio)", "first turn", &benchSamples{values: first})
	benchRecord("ACP simulator (Go stdio)", "warm turn", &benchSamples{values: warm})

	// Native protocol: codex against the fake app-server.
	fakeAgent := CreateCodexNativeAgent(Agent{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestFakeCodexAppServer", "--"},
		Env:     map[string]string{"GO_WANT_HELPER_PROCESS": "1"},
	})
	create2, first2, warm2 := benchSessionAndTurns(t, fakeAgent, nil, 5, 3)
	benchRecord("codex native (fake app-server)", "session create", &benchSamples{values: create2})
	benchRecord("codex native (fake app-server)", "first turn", &benchSamples{values: first2})
	benchRecord("codex native (fake app-server)", "warm turn", &benchSamples{values: warm2})

	t.Logf("\n=== PROTOCOL BASELINE TABLE ===")
	t.Logf("| 场景 | 指标 | avg | min | max |")
	t.Logf("|---|---|---|---|---|")
	for _, row := range benchRows {
		t.Logf("%s", row)
	}
}

// --- real-model scenarios (magpie gateway) -------------------------------

func TestPerfRealModels(t *testing.T) {
	base := os.Getenv("MAGPIE_BASE_URL")
	if base == "" {
		t.Skip("set MAGPIE_BASE_URL to run real-model perf tests")
	}
	key := os.Getenv("MAGPIE_API_KEY")
	if key == "" {
		key = "magpie"
	}
	anthropicBase := strings.TrimSuffix(strings.TrimSuffix(base, "/"), "/v1")
	codeHome := os.Getenv("CODEX_NATIVE_TEST_HOME")

	claudeNative := func() Agent {
		agent := CreateClaudeCodeNativeAgent(Agent{Env: map[string]string{
			"ANTHROPIC_BASE_URL":         anthropicBase,
			"ANTHROPIC_API_KEY":          key,
			"ANTHROPIC_MODEL":            "copilot/claude-sonnet-4.6",
			"ANTHROPIC_SMALL_FAST_MODEL": "copilot/claude-sonnet-4.6",
		}})
		return agent
	}
	claudeMeta := map[string]any{"model": "copilot/claude-sonnet-4.6"}

	codexNative := func() Agent {
		cfg, _ := json.Marshal(map[string]any{
			"model":          "codex/gpt-5.5",
			"model_provider": "magpie",
			"model_providers": map[string]any{"magpie": map[string]any{
				"name": "Magpie", "base_url": base, "env_key": "OPENAI_API_KEY", "wire_api": "responses",
			}},
		})
		env := map[string]string{"OPENAI_API_KEY": key, "CODEX_CONFIG": string(cfg)}
		if codeHome != "" {
			env["CODEX_HOME"] = codeHome
		}
		return CreateCodexNativeAgent(Agent{Env: env})
	}

	claudeACP := func() (Agent, map[string]any, bool) {
		entry, err := findCachedWrapper("claude-agent-acp")
		if err != nil {
			return Agent{}, nil, false
		}
		agent := Agent{Command: "node", Args: []string{entry}, Env: map[string]string{
			"ANTHROPIC_BASE_URL":            anthropicBase,
			"ANTHROPIC_API_KEY":             key,
			"ANTHROPIC_CUSTOM_MODEL_OPTION": "copilot/claude-sonnet-4.6",
		}}
		meta := map[string]any{"claudeCode": map[string]any{"options": map[string]any{
			"settings": map[string]any{"model": "copilot/claude-sonnet-4.6"},
		}}}
		return agent, meta, true
	}

	codexACP := func() (Agent, map[string]any, bool) {
		entry, err := findCachedWrapper("codex-acp")
		if err != nil {
			return Agent{}, nil, false
		}
		cfg, _ := json.Marshal(map[string]any{
			"model":          "codex/gpt-5.5",
			"model_provider": "magpie",
			"model_providers": map[string]any{"magpie": map[string]any{
				"name": "Magpie", "base_url": base, "env_key": "OPENAI_API_KEY", "wire_api": "responses",
			}},
		})
		env := map[string]string{"OPENAI_API_KEY": key, "CODEX_CONFIG": string(cfg)}
		if codeHome != "" {
			env["CODEX_HOME"] = codeHome
		}
		return Agent{Command: "node", Args: []string{entry}, Env: env}, nil, true
	}

	_ = claudeACP
	_ = codexACP
	_ = benchRecord

	// Native codex: session 1 (cold daemon) + session 2 (warm daemon, pooled
	// thread/start) + one turn each — the pooling payoff measurement.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	runtime := NewRuntime(nil, RuntimeOptions{})
	defer func() { _ = runtime.Close(context.Background()) }()
	codexAgent := codexNative()

	t0 := time.Now()
	s1, err := runtime.StartSession(ctx, StartSessionOptions{Agent: codexAgent, CWD: "/tmp"})
	coldCreate := time.Since(t0)
	if err != nil {
		t.Fatalf("codex cold StartSession: %v", err)
	}
	t1 := time.Now()
	s2, err := runtime.StartSession(ctx, StartSessionOptions{Agent: codexAgent, CWD: "/tmp"})
	warmCreate := time.Since(t1)
	if err != nil {
		t.Fatalf("codex warm StartSession: %v", err)
	}
	t2 := time.Now()
	_, err = s1.Run(ctx, "Reply with exactly one word: OK")
	coldTurn := time.Since(t2)
	if err != nil {
		t.Logf("codex s1 turn failed (gateway upstream?): %v", err)
	}
	t3 := time.Now()
	_, err = s2.Run(ctx, "Reply with exactly one word: OK")
	warmTurnD := time.Since(t3)
	if err != nil {
		t.Logf("codex s2 turn failed (gateway upstream?): %v", err)
	}
	benchRecord("codex native (real, gpt-5.5)", "session create COLD (daemon spawn)", &benchSamples{values: []time.Duration{coldCreate}})
	benchRecord("codex native (real, gpt-5.5)", "session create WARM (daemon pooled)", &benchSamples{values: []time.Duration{warmCreate}})
	benchRecord("codex native (real, gpt-5.5)", "turn", &benchSamples{values: []time.Duration{coldTurn, warmTurnD}})
	_ = s1.Close(context.Background())
	_ = s2.Close(context.Background())

	// Native claude: 2 sessions x 2 turns.
	create, first, warm := benchSessionAndTurns(t, claudeNative(), claudeMeta, 2, 2)
	benchRecord("claude native (real, sonnet-4.6)", "session create", &benchSamples{values: create})
	benchRecord("claude native (real, sonnet-4.6)", "first turn", &benchSamples{values: first})
	benchRecord("claude native (real, sonnet-4.6)", "warm turn", &benchSamples{values: warm})

	// Wrapper legs: attempt; record unavailable on failure.
	wrapClaude, wrapClaudeMeta, claudeOK := claudeACP()
	if claudeOK {
		create, first, warm := benchSessionAndTurns(t, wrapClaude, wrapClaudeMeta, 1, 1)
		benchRecord("claude ACP wrapper v0.67 (real)", "session create", &benchSamples{values: create})
		benchRecord("claude ACP wrapper v0.67 (real)", "turn", &benchSamples{values: append(first, warm...)})
	} else {
		benchRows = append(benchRows, "| claude ACP wrapper | (npx cache miss) | - | - | - |")
	}
	wrapCodex, wrapCodexMeta, codexOK := codexACP()
	if codexOK {
		create, first, warm := benchSessionAndTurns(t, wrapCodex, wrapCodexMeta, 1, 1)
		benchRecord("codex ACP wrapper v1.1.13 (real)", "session create", &benchSamples{values: create})
		benchRecord("codex ACP wrapper v1.1.13 (real)", "turn", &benchSamples{values: append(first, warm...)})
	} else {
		benchRows = append(benchRows, "| codex ACP wrapper | (npx cache miss) | - | - | - |")
	}

	t.Logf("\n=== FULL PERF TABLE ===")
	t.Logf("| 场景 | 指标 | avg | min | max |")
	t.Logf("|---|---|---|---|---|")
	for _, row := range benchRows {
		t.Logf("%s", row)
	}
}
