package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"

	acp "github.com/saaskit-dev/acp-runtime-go"
	"github.com/saaskit-dev/acp-runtime-go/harness"
)

func main() { os.Exit(run()) }

func run() int {
	var casePath string
	var agentID string
	var cwd string
	var simulatorBin string
	var all bool
	var jsonOutput string
	flag.StringVar(&casePath, "case", harness.DefaultCasePath("05-session-prompt.json"), "harness case JSON path")
	flag.StringVar(&agentID, "type", acp.LocalSimulatorAgentACPRegistryID, "agent registry id or alias")
	flag.StringVar(&cwd, "cwd", "", "session working directory")
	flag.StringVar(&simulatorBin, "simulator-bin", "", "path to acp-simulator-agent for local simulator cases")
	flag.BoolVar(&all, "all", false, "run all cases and report PASS, SKIPPED, UNSUPPORTED or FAIL individually")
	flag.StringVar(&jsonOutput, "json", "", "write complete machine-readable evidence to file")
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

	if agent.Type == acp.LocalSimulatorAgentACPRegistryID {
		storage, err := os.MkdirTemp("", "acp-harness-storage-")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		defer os.RemoveAll(storage)
		agent.Args = append(agent.Args, "--storage-dir", storage)
	}
	paths := []string{casePath}
	if all {
		paths, err = filepath.Glob(harness.DefaultCasePath("*.json"))
		if err != nil {
			fatal(err)
		}
	}
	results := make([]harness.Result, 0, len(paths))
	exitCode := 0
	for _, path := range paths {
		result, runErr := harness.RunCaseFile(ctx, path, agent, cwd)
		if runErr != nil {
			var unsupported *harness.UnsupportedError
			if errors.As(runErr, &unsupported) {
				if exitCode == 0 {
					exitCode = 2
				}
			} else {
				exitCode = 1
			}
			result.Reason = runErr.Error()
		}
		results = append(results, result)
		fmt.Printf("%s %s %s\n", result.Status, result.CaseID, result.Reason)
	}
	if jsonOutput != "" {
		data, err := json.MarshalIndent(results, "", "  ")
		if err != nil {
			fatal(err)
		}
		if err := os.WriteFile(jsonOutput, append(data, '\n'), 0600); err != nil {
			fatal(err)
		}
	}
	return exitCode

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
	if absolute, err := filepath.Abs(simulatorBin); err == nil {
		simulatorBin = absolute
	}
	return acp.Agent{Type: acp.LocalSimulatorAgentACPRegistryID, Command: simulatorBin, Args: []string{"--auth-mode", "optional"}}, nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
