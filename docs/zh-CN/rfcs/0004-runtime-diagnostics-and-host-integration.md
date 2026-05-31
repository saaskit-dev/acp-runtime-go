# RFC-0004：Runtime Diagnostics And Host Integration

[English](../../rfcs/0004-runtime-diagnostics-and-host-integration.md)

## 决策

Go runtime diagnostics 保持克制：

- typed `RuntimeError`，包含 `Kind`、`Op`、`Msg`、`Cause`
- 通过 `StdioFactoryOptions.OnACPMessage` 采集 raw ACP message
- 使用 deterministic simulator 和 harness validation 作为行为证据

宿主应在 transport 边界接入自己的 logging、tracing 和 redaction policy。没有明确产品策略时，不应采集 prompt 与 tool content。

## 集成边界

宿主代码调用 `Runtime` 和 `Session`。除非在实现自定义 transport 或 protocol diagnostic tool，否则不应直接操作 raw JSON-RPC。
