# Runtime SDK By Scenario

Language:
- English (default)
- [简体中文](../zh-CN/guides/runtime-sdk-by-scenario.md)

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

Use `ListSessions`, `LoadSession`, and `ResumeSession` with the same resolved
agent and working directory. Local durable registry support is intentionally
kept narrow in the current Go port; host products should persist their own
product lifecycle state.
