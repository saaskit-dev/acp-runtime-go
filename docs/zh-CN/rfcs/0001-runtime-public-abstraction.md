# RFC-0001：Runtime Public Abstraction

[English](../../rfcs/0001-runtime-public-abstraction.md)

## 决策

Go 公开 surface 以 `Runtime` 和 `Session` 为中心。

ACP-specific 编排位于内部 runtime 边界后面：

- `SessionService`：initialize、authentication、session lifecycle 和 cleanup。
- `SessionDriver`：归一化 turn execution 和 read-model state。
- `AgentProfile`：按 `Agent.Type` 选择的兼容策略。
- `Connection`：基于 stdio 或自定义 factory 的 ACP JSON-RPC/NDJSON client。

## 理由

宿主需要稳定的 ACP runtime 模型，而不是 raw per-agent protocol handling。runtime 应在 `Agent.Type` 已知后隐藏实现差异，同时保持 ACP-focused thin facade。

## 当前布局

- `runtime.go`：宿主侧 runtime 入口。
- `session.go`：宿主侧 session handle。
- `session_service.go`：ACP lifecycle 编排。
- `session_driver.go`：turn execution 与 read-model normalization。
- `connection.go`、`rpc.go`、`stdio.go`：ACP transport。
- `profiles.go`：兼容策略。
- `registry.go`、`agents.go`：启动解析。
- `simulator/`：确定性 ACP agent。
