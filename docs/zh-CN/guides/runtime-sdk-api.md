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

所有 Agent 共用一套宿主 API。在 Create / Resume 上用 Meta 辅助函数声明意图；
runtime 在内部完成各 Provider 的投影，宿主无需关心实现细节。

```go
// 替换 Provider system prompt
options.Meta = acp.WithSystemPrompt("你是当前工作区的编码 Agent。")

// 或追加
options.Meta = acp.WithAppendSystemPrompt("优先小范围改动。")

// 与其它 session metadata 合并
options.Meta = acp.MergeMeta(
	acp.WithSystemPrompt("你是当前工作区的编码 Agent。"),
	map[string]any{"customKey": "value"},
)
```

规则：

- 替换：`WithSystemPrompt`（键 `SystemPromptMetaKey`）
- 追加：`WithAppendSystemPrompt`（键 `AppendSystemPromptMetaKey`）
- 两个非空值互斥
- 值必须是字符串；空白字符串按未设置处理
- **不要**通过 `Agent.Args`、`Agent.Env`、`CreateCodexConfig` / `CODEX_CONFIG`、
  Claude CLI flag 或 `AgentConfig` 注入 system prompt

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
