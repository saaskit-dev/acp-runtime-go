# RFC-0003：Runtime Snapshot, Policy, And Recovery

[English](../../rfcs/0003-runtime-snapshot-policy-and-recovery.md)

## 决策

`RuntimeSnapshot` 是最小恢复模型：

- snapshot version
- resolved `Agent`
- cwd
- MCP servers
- ACP session id
- current mode id
- 通过 runtime 应用的 raw config values

当前 Go port 的 durable local registry 支持保持窄边界。产品宿主应持久化自己的产品生命周期状态，并在 load/resume session 时传入 resolved `Agent`、cwd 和 handlers。

## Policy

runtime policy 名称保持精确：

- `yolo`
- `accept-edits`
- `read-only`

raw agent mode 与 runtime policy projection 保持分离。
