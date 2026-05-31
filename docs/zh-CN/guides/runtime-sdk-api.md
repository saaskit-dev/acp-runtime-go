# Runtime SDK API

[English](../../guides/runtime-sdk-api.md)

公开 Go package 是 `github.com/saaskit-dev/acp-runtime-go`。

## 主要类型

- `Runtime`：宿主侧入口。
- `Session`：宿主侧 session handle。
- `SessionDriver`：内部归一化边界。
- `Agent`：已解析的 ACP agent 启动配置。
- `AgentProfile`：per-agent 兼容策略。

## Runtime 构造

```go
runtime := acp.NewRuntime(
	acp.NewStdioConnectionFactory(acp.StdioFactoryOptions{}),
	acp.RuntimeOptions{},
)
```

核心方法：

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

常用 alias 包括 `claude`、`codex`、`copilot`、`github`、`pi`、`sim`、`simulator`。

## Runtime Policy Names

公共 policy 词汇保持精确：

- `yolo`
- `accept-edits`
- `read-only`

raw agent mode 与 runtime policy projection 保持分离。宿主应优先使用 runtime-level options，除非明确拥有某个 agent integration，否则不要写 per-agent 分支。
