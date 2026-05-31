# Runtime SDK Minimal Demo

Language:
- English (default)
- [简体中文](../zh-CN/guides/runtime-sdk-minimal-demo.md)

```go
runtime := acp.NewRuntime(acp.NewStdioConnectionFactory(acp.StdioFactoryOptions{}), acp.RuntimeOptions{})

agent := acp.Agent{
	Type:    acp.LocalSimulatorAgentACPRegistryID,
	Command: "bin/acp-simulator-agent",
	Args:    []string{"--auth-mode", "none"},
}

session, err := runtime.StartSession(ctx, acp.StartSessionOptions{Agent: agent, CWD: cwd})
if err != nil {
	return err
}
defer session.Close(ctx)

completion, err := session.Run(ctx, "Reply with the single word OK.")
```
