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
- ACP source ref: `v0.13.4`
- Last verified against upstream docs: `2026-06-01`

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
- `bin/acp-openai-server`
- `bin/acp-simulator-agent`
- `bin/acp-harness`

## OpenAI-Compatible Server

The repository includes a local OpenAI-compatible HTTP gateway:

```bash
./run openai-server
```

Main endpoints:

- `GET /v1/models`
- `POST /v1/chat/completions`
- `GET /v1/acp/sessions`
- `DELETE /v1/acp/sessions/{id}`

`GET /v1/models` returns routable OpenAI model IDs. A model ID may include an
ACP agent prefix:

- `claude/sonnet`: start the `claude` ACP agent and set ACP config `model=sonnet`
- `codex/gpt-5.5`: start the `codex` ACP agent and set ACP config `model=gpt-5.5`
- `gpt-5.5`: use the first `--agents` entry and set ACP config `model=gpt-5.5`

When `--discover-models` is enabled, the server probes the agents listed by
`--agents`, reads ACP model metadata or the `model` config option choices, closes
the probe sessions immediately, and caches the discovered model IDs according to
`--model-discovery-ttl`. Explicit `--models` entries are always included and are
used as the fallback if an agent does not expose model metadata.

By default, `--agents` is `claude,codex`, model discovery is enabled, and `--cwd`
is the user home directory. The optional `--agent` flag can override the default
agent, but the normal path is to use `--agents`; its first entry is the default.

Without `X-ACP-Session-ID`, requests create a temporary ACP session and close it after one turn. To reuse an ACP session, send `X-ACP-Session-Mode: persistent` on the first request; the server returns `X-ACP-Session-ID` in the response headers:

```bash
curl -i http://127.0.0.1:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -H 'X-ACP-Session-Mode: persistent' \
  -d '{"model":"claude/sonnet","messages":[{"role":"user","content":"Summarize this repository."}]}'
```

Follow-up requests with the same `X-ACP-Session-ID` reuse the ACP session. When reusing a session, the default input mode sends only the last user message as the next turn, so OpenAI clients that replay the full `messages` array do not duplicate history inside the ACP session. Use `X-ACP-Input-Mode: replay` to explicitly send the full replayed context.

Only one turn may run at a time per ACP session; concurrent requests return `session_busy`. Sessions are isolated by API key/owner, agent, model, cwd, and system prompt hash, and expire according to `--session-ttl`.

`GET /v1/acp/sessions` returns both the gateway session id and the underlying `acp_session_id`, so operators can see which sessions were opened and are managed by this gateway. On shutdown, the gateway closes every registered managed ACP session and the runtime it created. `SIGINT`, `SIGTERM`, periodic TTL cleanup, and `DELETE /v1/acp/sessions/{id}` all trigger cleanup so local ACP agent processes do not leak.

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

## Performance Notes

The default RPC APIs accept ordinary Go structs and keep the host integration simple:
`Peer.Call(ctx, method, params, &result)` and `Peer.Notify(ctx, method, params)`.
High-throughput hosts that already hold encoded JSON can use `Peer.CallRaw` and
`Peer.NotifyRaw` with `json.RawMessage` to avoid interface boxing and repeated parameter
marshalling on hot paths.

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
