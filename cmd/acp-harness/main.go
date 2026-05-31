package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"

	acp "github.com/saaskit-dev/acp-runtime-go"
	"github.com/saaskit-dev/acp-runtime-go/harness"
)

func main() {
	var casePath string
	var agentID string
	var cwd string
	var simulatorBin string
	flag.StringVar(&casePath, "case", harness.DefaultCasePath("05-session-prompt.json"), "harness case JSON path")
	flag.StringVar(&agentID, "type", acp.LocalSimulatorAgentACPRegistryID, "agent registry id or alias")
	flag.StringVar(&cwd, "cwd", "", "session working directory")
	flag.StringVar(&simulatorBin, "simulator-bin", "", "path to acp-simulator-agent for local simulator cases")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			fatal(err)
		}
	}
	agent, err := resolveAgent(ctx, agentID, simulatorBin)
	if err != nil {
		fatal(err)
	}
	result, err := harness.RunCaseFile(ctx, casePath, agent, cwd)
	if err != nil {
		fatal(err)
	}
	fmt.Printf("ok %s\n", result.CaseID)
}

func resolveAgent(ctx context.Context, agentID string, simulatorBin string) (acp.Agent, error) {
	if acp.ResolveRuntimeAgentID(agentID) != acp.LocalSimulatorAgentACPRegistryID {
		return acp.ResolveRuntimeAgentFromRegistry(ctx, agentID)
	}
	if simulatorBin == "" {
		bin := filepath.Join(os.TempDir(), "acp-simulator-agent-go")
		cmd := exec.CommandContext(ctx, "go", "build", "-o", bin, "./cmd/acp-simulator-agent")
		if output, err := cmd.CombinedOutput(); err != nil {
			return acp.Agent{}, fmt.Errorf("build simulator: %w\n%s", err, string(output))
		}
		simulatorBin = bin
	}
	return acp.Agent{Type: acp.LocalSimulatorAgentACPRegistryID, Command: simulatorBin, Args: []string{"--auth-mode", "none"}}, nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
