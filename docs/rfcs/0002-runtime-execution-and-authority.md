# RFC-0002: Runtime Execution And Authority

Language:
- English (default)
- [简体中文](../zh-CN/rfcs/0002-runtime-execution-and-authority.md)

## Decision

Runtime execution flows through:

`Runtime -> SessionService -> Connection -> ACP agent -> SessionDriver -> Session`

Authority remains host-provided:

- permission decisions
- filesystem reads and writes
- terminal execution
- authentication choices that require user or host policy

The runtime may auto-run only safe protocol-level `agent` authentication. It must
not auto-run terminal or environment-variable authentication without host
participation.

## Lifecycle

1. Resolve `Agent` from an explicit config or registry id.
2. Spawn/connect through a `ConnectionFactory`.
3. Send `initialize`.
4. Normalize auth methods through `AgentProfile`.
5. Create, load, resume, fork, or list a session.
6. Route `session/update` notifications into normalized turn events and read models.
7. Dispose the agent process on every failure after spawn.

## Failure Rule

Any path that starts an agent process must dispose that process if initialize,
auth, or session creation fails.
