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

## System Prompt 契约

宿主在 Create 和 Resume 中统一通过 `StartSessionOptions.Meta` 声明 system
prompt 意图：

```go
options.Meta = map[string]any{
	acp.SystemPromptMetaKey: "你是当前工作区的编码 Agent。",
}
```

- `SystemPromptMetaKey`（`_meta.systemPrompt`）表示替换 Provider system prompt。
- `AppendSystemPromptMetaKey`（`_meta.appendSystemPrompt`）表示追加到 Provider system prompt。
- 两个键的非空值互斥；值必须是字符串，空白字符串按未设置处理。
- runtime profile 层只选择一种 Provider 原生投影。Claude Code 的 replace
  转换为 `--system-prompt`，append 转换为 `--append-system-prompt`，消费后的
  prompt 键不会再通过 ACP `_meta` 重复发送。

调用方不得在 `Agent.Args` 中另外维护 Provider 专属 prompt 参数。

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
