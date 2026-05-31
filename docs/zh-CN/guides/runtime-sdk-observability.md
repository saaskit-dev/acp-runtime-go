# Runtime SDK Observability

[English](../../guides/runtime-sdk-observability.md)

Go port 的第一版 observability 保持克制：

- 可通过 `StdioFactoryOptions.OnACPMessage` 采集 ACP raw message。
- runtime error 使用 `RuntimeError`，包含 `Kind`、`Op`、`Msg`、`Cause`。
- 确定性 simulator 与 harness run 是主要行为证据。

示例：

```go
factory := acp.NewStdioConnectionFactory(acp.StdioFactoryOptions{
	OnACPMessage: func(direction string, message []byte) {
		// Attach to your host logger.
	},
})
runtime := acp.NewRuntime(factory, acp.RuntimeOptions{})
```

除非宿主有明确的内容采集和脱敏策略，否则不要记录 prompt 或 tool content。
