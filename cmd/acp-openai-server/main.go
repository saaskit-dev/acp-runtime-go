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
	var cwd string
	var apiKey string
	var ttl time.Duration
	var allowHeaderCWD bool
	var models string

	flag.StringVar(&listen, "listen", "127.0.0.1:8080", "HTTP listen address")
	flag.StringVar(&agentID, "agent", acp.LocalSimulatorAgentACPRegistryID, "default ACP agent id or alias")
	flag.StringVar(&cwd, "cwd", "", "session working directory")
	flag.StringVar(&apiKey, "api-key", "", "optional API key required as Bearer token or X-API-Key")
	flag.DurationVar(&ttl, "session-ttl", 30*time.Minute, "persistent ACP session TTL")
	flag.BoolVar(&allowHeaderCWD, "allow-header-cwd", false, "allow X-ACP-CWD to override working directory")
	flag.StringVar(&models, "models", "", "comma-separated model ids returned by /v1/models")
	flag.Parse()

	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			log.Fatal(err)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	server := openaiserver.NewServer(openaiserver.Config{
		ConnectionFactory: acp.NewStdioConnectionFactory(acp.StdioFactoryOptions{Stderr: "inherit"}),
		DefaultAgentID:    agentID,
		ResolveAgent:      resolveAgent,
		CWD:               cwd,
		SessionTTL:        ttl,
		APIKey:            apiKey,
		AllowHeaderCWD:    allowHeaderCWD,
		Models:            splitCSV(models, agentID),
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

func splitCSV(value string, fallback string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	var out []string
	for _, item := range strings.Split(value, ",") {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	if len(out) == 0 && fallback != "" {
		out = append(out, fallback)
	}
	return out
}
