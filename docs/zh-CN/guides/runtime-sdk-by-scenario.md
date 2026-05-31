# Runtime SDK 分场景接入

[English](../../guides/runtime-sdk-by-scenario.md)

## Minimal Session

```go
runtime := acp.NewRuntime(acp.NewStdioConnectionFactory(acp.StdioFactoryOptions{}), acp.RuntimeOptions{})
agent := acp.CreateClaudeCodeAgent(acp.Agent{})
session, err := runtime.StartSession(ctx, acp.StartSessionOptions{Agent: agent, CWD: cwd})
```

## One-Shot Prompt

```go
completion, err := session.Run(ctx, "Summarize this repository.")
```

## Streaming Turn Events

```go
turn := session.StartTurn(ctx, acp.RuntimePrompt{Text: "Plan the change."})
for event := range turn.Events {
	_ = event
}
result := <-turn.Completion
```

## Agent Control

```go
_ = session.SetAgentMode(ctx, "accept-edits")
_ = session.SetAgentConfigOption(ctx, "model", "gpt")
```

## Stored Or Remote Sessions

用 `ListSessions`、`LoadSession`、`ResumeSession` 配合同一个 resolved agent 和 cwd。当前 Go port 的本地 durable registry 支持保持窄边界；产品宿主应持久化自己的产品生命周期状态。
