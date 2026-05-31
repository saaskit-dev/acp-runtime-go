# Runtime SDK Observability

[English](../../guides/runtime-sdk-observability.md)

Go port 的第一版 observability 保持克制：

- 可通过 `StdioFactoryOptions.OnACPMessage` 采集 ACP raw message。
- runtime error 使用 `RuntimeError`，包含 `Kind`、`Op`、`Msg`、`Cause`。
- 可通过 `RuntimeOptions.Observability.OnProtocolError` 观察协议解码失败。
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

协议错误回调示例：

```go
runtime := acp.NewRuntime(factory, acp.RuntimeOptions{
	Observability: acp.ObservabilityOptions{
		CaptureContent: "raw",
		OnProtocolError: func(ctx acp.Context, event acp.ProtocolErrorEvent) {
			// 将 event.Method 和 event.Err 接入宿主日志。
			// 只有 CaptureContent 允许 raw content 时，event.Raw 才会填充。
		},
	},
})
```

除非宿主有明确的内容采集和脱敏策略，否则不要记录 prompt 或 tool content。
