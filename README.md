# acp-runtime-go

Language:
- English (default)
- [简体中文](README.zh-CN.md)

## Overview

`acp-runtime` is a Go host-side runtime layer for Agent Client Protocol (ACP) products.
It gives hosts one stable model for agent launch, sessions, turns, registry resolution,
profile compatibility, stdio transport, read models, and deterministic simulator validation.

The runtime remains ACP-focused. It is a thin runtime/facade over ACP agents, not a separate
agent backend.

Protocol alignment:

- ACP protocol version: `1`
- ACP source repo: `https://github.com/agentclientprotocol/agent-client-protocol`
- ACP source ref: `v0.11.4`
- Last verified against upstream docs: `2026-04-08`

## Quick Start

```go
package main

import (
	"context"
	"fmt"
	"os"

	acp "github.com/saaskit-dev/acp-runtime-go"
)

func main() {
	ctx := context.Background()
	runtime := acp.NewRuntime(acp.NewStdioConnectionFactory(acp.StdioFactoryOptions{}), acp.RuntimeOptions{})

	agent := acp.CreateClaudeCodeAgent(acp.Agent{})
	session, err := runtime.StartSession(ctx, acp.StartSessionOptions{
		Agent: agent,
		CWD:   mustGetwd(),
	})
	if err != nil {
		panic(err)
	}
	defer session.Close(ctx)

	completion, err := session.Run(ctx, "Summarize the current workspace.")
	if err != nil {
		panic(err)
	}
	fmt.Println(completion.OutputText)
}

func mustGetwd() string {
	cwd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	return cwd
}
```

## Commands

```bash
make build
make test
make lint
./run runtime simulator
./run harness --case harness/cases/05-session-prompt.json
```

Built binaries are written to `bin/`:

- `bin/acp-runtime`
- `bin/acp-simulator-agent`
- `bin/acp-harness`

## Repository Layout

- Root package: Go runtime SDK, ACP protocol types, stdio transport, registry resolution, profiles, and session driver.
- `cmd/acp-runtime/`: maintained interactive runtime CLI.
- `cmd/acp-simulator-agent/`: deterministic ACP stdio simulator agent.
- `cmd/acp-harness/`: Go harness runner for repository case files.
- `simulator/`: simulator agent implementation.
- `harness/`: Go harness package and JSON case definitions.
- `docs/`: public guides, RFCs, compatibility notes, and research records.

## Runtime Concepts

- `Runtime`: host-facing entry point for starting, loading, resuming, forking, listing, and closing sessions.
- `Session`: host-facing session object for turns, agent config, snapshots, and read models.
- `SessionDriver`: internal boundary that normalizes ACP session behavior behind `Session`.
- `AgentProfile`: compatibility policy selected by `Agent.Type`.

Public runtime policy names remain exact: `yolo`, `accept-edits`, and `read-only`.

## Simulator

The Go simulator is a deterministic ACP stdio agent. It supports:

- `initialize`, `authenticate`, `session/new`, `session/list`, `session/load`, `session/resume`, `session/fork`
- `session/prompt`, `session/update`, `session/set_mode`, `session/set_config_option`, `session/close`
- deterministic prompt actions for describing, planning, reading, writing, running, and renaming sessions

Run it directly:

```bash
./run simulator --auth-mode none
```

## Development

```bash
make clean
make build
make lint
make test
make harness-admission
```

Generated runtime state defaults to `~/.acp-runtime/`.
Override the home root with `ACP_RUNTIME_HOME_DIR`, and cache-only paths with `ACP_RUNTIME_CACHE_DIR`.
