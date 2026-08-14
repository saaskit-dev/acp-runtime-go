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

## 后台 Session Updates

`session/prompt` 返回后会清掉 in-flight turn。之后到达的 `session/update`
（例如 Claude 后台 bash 完成后的 follow-up 文本）不再进入 turn events。
需要从 `Session.Updates()` 消费这些 orphan 通知；缓冲区满时会丢弃并触发
`OnEventDrop`。按 `UpdateID` 分组（协议 `messageId`，否则 `orphan:{session}:{n}`）。
`Terminal` 为 true 表示该 generation 结束：`available_commands_update`、新的
`messageId`，或下一次 `StartTurn`。

```go
updates := session.Updates()
go func() {
	for {
		select {
		case <-ctx.Done():
			return
		case notification, ok := <-updates:
			if !ok {
				return
			}
			_ = notification
		}
	}
}()
```

## Agent Control

```go
_ = session.SetAgentMode(ctx, "accept-edits")
_ = session.SetAgentConfigOption(ctx, "model", "gpt")
```

## Stored Or Remote Sessions

用 `ListSessions`、`LoadSession`、`ResumeSession` 配合同一个 resolved agent 和 cwd。当前 Go port 的本地 durable registry 支持保持窄边界；产品宿主应持久化自己的产品生命周期状态。
