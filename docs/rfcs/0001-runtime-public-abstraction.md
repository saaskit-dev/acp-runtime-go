# RFC-0001: Runtime Public Abstraction

Language:
- English (default)
- [简体中文](../zh-CN/rfcs/0001-runtime-public-abstraction.md)

## Decision

The Go public surface is centered on `Runtime` and `Session`.

ACP-specific orchestration remains behind internal runtime boundaries:

- `SessionService`: initialize, authentication, session lifecycle, and cleanup.
- `SessionDriver`: normalized turn execution and read-model state.
- `AgentProfile`: compatibility policy selected by `Agent.Type`.
- `Connection`: ACP JSON-RPC/NDJSON client over stdio or another factory.

## Rationale

Hosts need a stable ACP runtime model, not raw per-agent protocol handling. The
runtime should hide implementation differences after `Agent.Type` is known while
remaining a thin ACP-focused facade.

## Current Layout

- `runtime.go`: host-facing runtime entry point.
- `session.go`: host-facing session handle.
- `session_service.go`: ACP lifecycle orchestration.
- `session_driver.go`: turn execution and read-model normalization.
- `connection.go`, `rpc.go`, `stdio.go`: ACP transport.
- `profiles.go`: compatibility policy.
- `registry.go`, `agents.go`: launch resolution.
- `simulator/`: deterministic ACP agent.
