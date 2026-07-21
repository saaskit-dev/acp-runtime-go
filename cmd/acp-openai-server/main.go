package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	acp "github.com/saaskit-dev/acp-runtime-go"
	openaiserver "github.com/saaskit-dev/acp-runtime-go/openai"
)

func main() {
	var listen string
	var agentID string
	var agentSet bool
	var cwd string
	var apiKey string
	var ttl time.Duration
	var maxSessions int
	var allowHeaderCWD bool
	var models string
	var agents string
	var discoverModels bool
	var modelDiscoveryTTL time.Duration

	flag.StringVar(&listen, "listen", "127.0.0.1:8080", "HTTP listen address")
	flag.StringVar(&agentID, "agent", "", "default ACP agent id or alias; defaults to the first --agents entry")
	flag.StringVar(&cwd, "cwd", "", "session working directory; defaults to the user home directory")
	flag.StringVar(&apiKey, "api-key", "", "optional API key required as Bearer token or X-API-Key")
	flag.DurationVar(&ttl, "session-ttl", 30*time.Minute, "persistent ACP session TTL")
	flag.IntVar(&maxSessions, "max-sessions", 256, "maximum concurrent managed sessions; 0 uses default, negative disables")
	flag.BoolVar(&allowHeaderCWD, "allow-header-cwd", false, "allow X-ACP-CWD to override working directory")
	flag.StringVar(&models, "models", "", "comma-separated OpenAI model ids returned by /v1/models, e.g. claude/sonnet,codex/gpt-5.5")
	flag.StringVar(&agents, "agents", "claude,codex", "comma-separated ACP agent ids or aliases; first entry is the default agent")
	flag.BoolVar(&discoverModels, "discover-models", true, "probe ACP agents and add discovered models to /v1/models")
	flag.DurationVar(&modelDiscoveryTTL, "model-discovery-ttl", 10*time.Minute, "TTL for discovered /v1/models entries")
	flag.Parse()
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "agent" {
			agentSet = true
		}
	})
	agentIDs := splitCSV(agents)
	if len(agentIDs) == 0 {
		agentIDs = []string{acp.LocalSimulatorAgentACPRegistryID}
	}
	if !agentSet || strings.TrimSpace(agentID) == "" {
		agentID = agentIDs[0]
	}

	if cwd == "" {
		var err error
		cwd, err = os.UserHomeDir()
		if err != nil {
			log.Fatal(err)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	server := openaiserver.NewServer(openaiserver.Config{
		ConnectionFactory:          acp.NewStdioConnectionFactory(acp.StdioFactoryOptions{Stderr: "inherit"}),
		DiscoveryConnectionFactory: acp.NewStdioConnectionFactory(acp.StdioFactoryOptions{Stderr: "ignore"}),
		DefaultAgentID:             agentID,
		ResolveAgent:               resolveAgent,
		CWD:                        cwd,
		SessionTTL:                 ttl,
		MaxSessions:                maxSessions,
		APIKey:                     apiKey,
		AllowHeaderCWD:             allowHeaderCWD,
		Models:                     splitCSV(models),
		Agents:                     agentIDs,
		DiscoverModels:             discoverModels,
		ModelDiscoveryTTL:          modelDiscoveryTTL,
		AccessLog: func(entry openaiserver.AccessLogEntry) {
			log.Printf("%s %s status=%d dur=%s session=%s remote=%s",
				entry.Method, entry.Path, entry.Status, entry.Duration.Round(time.Millisecond), entry.SessionID, entry.RemoteAddr)
		},
	})

	httpServer := &http.Server{Addr: listen, Handler: server.Handler()}
	defer server.Close(context.Background())
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
		_ = server.Close(shutdownCtx)
	}()

	fmt.Printf("acp-openai-server listening on http://%s\n", listen)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func resolveAgent(ctx context.Context, agentID string) (acp.Agent, error) {
	agent, err := acp.ResolveRuntimeAgentFromRegistry(ctx, agentID)
	if err != nil {
		return acp.Agent{}, err
	}
	if agent.Type != acp.LocalSimulatorAgentACPRegistryID {
		return agent, nil
	}
	if agent.Command != "acp-simulator-agent" && agent.Command != "" {
		return agent, nil
	}
	if exe, err := os.Executable(); err == nil {
		sibling := filepath.Join(filepath.Dir(exe), "acp-simulator-agent")
		if info, statErr := os.Stat(sibling); statErr == nil && !info.IsDir() {
			agent.Command = sibling
			return agent, nil
		}
	}
	tempBin := filepath.Join(os.TempDir(), "acp-simulator-agent-go")
	cmd := exec.Command("go", "build", "-o", tempBin, "./cmd/acp-simulator-agent")
	if output, err := cmd.CombinedOutput(); err != nil {
		return agent, fmt.Errorf("build local simulator: %w\n%s", err, string(output))
	}
	agent.Command = tempBin
	return agent, nil
}

func splitCSV(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	var out []string
	for _, item := range strings.Split(value, ",") {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
