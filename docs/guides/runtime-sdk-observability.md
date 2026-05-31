# Runtime SDK Observability

Language:
- English (default)
- [简体中文](../zh-CN/guides/runtime-sdk-observability.md)

The Go port keeps observability intentionally small in the first public surface.

- ACP raw message tapping is available through `StdioFactoryOptions.OnACPMessage`.
- Runtime errors use `RuntimeError` with `Kind`, `Op`, `Msg`, and `Cause`.
- Deterministic simulator and harness runs are the primary behavioral evidence.

Example:

```go
factory := acp.NewStdioConnectionFactory(acp.StdioFactoryOptions{
	OnACPMessage: func(direction string, message []byte) {
		// Attach to your host logger.
	},
})
runtime := acp.NewRuntime(factory, acp.RuntimeOptions{})
```

Do not log prompt or tool content unless the host has an explicit product policy
for content capture and redaction.
