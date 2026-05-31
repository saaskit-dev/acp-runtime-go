package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"

	acp "github.com/saaskit-dev/acp-runtime-go"
)

func main() {
	var listAgents bool
	var cwd string
	flag.BoolVar(&listAgents, "list-agents", false, "list agents from ACP registry")
	flag.StringVar(&cwd, "cwd", "", "session working directory")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if listAgents {
		agents, err := acp.ListRuntimeRegistryAgents(ctx)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		for _, agent := range agents {
			fmt.Printf("%s\t%s\t%s\n", agent.ID, agent.Version, agent.Name)
		}
		return
	}

	agentID := acp.LocalSimulatorAgentACPRegistryID
	if flag.NArg() > 0 {
		agentID = flag.Arg(0)
	}
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	agent, err := acp.ResolveRuntimeAgentFromRegistry(ctx, agentID)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	agent, err = resolveLocalSimulator(agent)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	runtime := acp.NewRuntime(acp.NewStdioConnectionFactory(acp.StdioFactoryOptions{}), acp.RuntimeOptions{})
	session, err := runtime.StartSession(ctx, acp.StartSessionOptions{Agent: agent, CWD: cwd})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer session.Close(context.Background())
	fmt.Printf("started %s in %s\n", session.Snapshot().Session.ID, cwd)
	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("> ")
		if !scanner.Scan() {
			break
		}
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			continue
		}
		if text == "/exit" || text == "/quit" {
			break
		}
		completion, err := session.Run(ctx, text)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			continue
		}
		fmt.Println(completion.OutputText)
	}
}

func resolveLocalSimulator(agent acp.Agent) (acp.Agent, error) {
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
