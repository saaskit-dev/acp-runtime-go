# RFC-0002：Runtime Execution And Authority

[English](../../rfcs/0002-runtime-execution-and-authority.md)

## 决策

Runtime 执行链路是：

`Runtime -> SessionService -> Connection -> ACP agent -> SessionDriver -> Session`

Authority 仍由宿主提供：

- permission decisions
- filesystem reads and writes
- terminal execution
- 需要用户或宿主策略参与的 authentication choices

runtime 只允许自动执行安全的 protocol-level `agent` authentication。不得在没有宿主参与时自动执行 terminal 或 environment-variable authentication。

## 生命周期

1. 从显式配置或 registry id 解析 `Agent`。
2. 通过 `ConnectionFactory` spawn/connect。
3. 发送 `initialize`。
4. 通过 `AgentProfile` 归一化 auth methods。
5. create/load/resume/fork/list session。
6. 把 `session/update` notification 映射为归一化 turn events 和 read models。
7. spawn 后任一步失败，都必须 dispose agent process。
