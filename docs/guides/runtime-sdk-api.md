# Runtime SDK API

Language:
- English (default)
- [简体中文](../zh-CN/guides/runtime-sdk-api.md)

The public Go package is `github.com/saaskit-dev/acp-runtime-go`.

## Primary Types

- `Runtime`: host-facing entry point.
- `Session`: host-facing session handle.
- `SessionDriver`: internal normalization boundary.
- `Agent`: resolved ACP agent launch config.
- `AgentProfile`: per-agent compatibility policy.

## Runtime Construction

```go
runtime := acp.NewRuntime(
	acp.NewStdioConnectionFactory(acp.StdioFactoryOptions{}),
	acp.RuntimeOptions{},
)
```

Core methods:

- `StartSession(ctx, StartSessionOptions)`
- `LoadSession(ctx, LoadSessionOptions)`
- `ResumeSession(ctx, ResumeSessionOptions)`
- `ForkSession(ctx, ForkSessionOptions)`
- `ListSessions(ctx, ListSessionsOptions)`
- `Close(ctx)`

## Session Surface

- `Run(ctx, text)`
- `StartTurn(ctx, RuntimePrompt)`
- `CancelTurn(ctx, turnID)`
- `SetAgentMode(ctx, modeID)`
- `SetAgentConfigOption(ctx, id, value)`
- `Snapshot()`
- `Metadata()`
- `Capabilities()`
- `ThreadEntries()`
- `ToolCalls()`
- `Operations()`
- `PermissionRequests()`
- `Close(ctx)`

## Registry And Agents

- `ResolveRuntimeAgentID(id)`
- `ResolveRuntimeAgentFromRegistry(ctx, id)`
- `ListRuntimeRegistryAgents(ctx)`
- `CreateCodexAgent(overrides)`
- `CreateClaudeCodeAgent(overrides)`
- `CreateGeminiAgent(overrides)`
- `CreateGitHubCopilotAgent(overrides)`
- `CreateOpenCodeAgent(overrides)`
- `CreatePiAgent(overrides)`

Common aliases include `claude`, `codex`, `copilot`, `github`, `pi`, `sim`, and
`simulator`.

## Runtime Policy Names

The public policy vocabulary remains exact:

- `yolo`
- `accept-edits`
- `read-only`

Raw agent modes and runtime policy projection stay separate. Hosts should prefer
runtime-level options and avoid agent-specific branches unless they own that
agent integration explicitly.
