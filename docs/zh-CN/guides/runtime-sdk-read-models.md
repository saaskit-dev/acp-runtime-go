# Runtime SDK 读模型

[English](../../guides/runtime-sdk-read-models.md)

Go runtime 通过 `Session` 暴露只读快照：

- `ThreadEntries()` 返回用户和 assistant 可见历史。
- `ToolCalls()` 返回归一化后的 ACP tool call snapshot。
- `Operations()` 返回从 tool call 派生的 host-facing action projection。
- `PermissionRequests()` 返回 agent 请求宿主审批时产生的权限 projection。
- `Metadata()` 返回 session 标题、mode、config 和 available command metadata。
- `Snapshot()` 返回最小恢复模型。

当前 Go port 暂不把 watcher API 放入第一版公开 surface。宿主可通过 `StartTurn` 的事件消费 live update，并在需要当前状态时读取 snapshot。
