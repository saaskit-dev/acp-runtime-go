# Runtime Agent Compatibility

[English](../../guides/runtime-agent-compatibility.md)

Agent-specific 行为应放在 `AgentProfile` 或 registry resolution 里，而不是散落在宿主集成代码中。

当前 Go profiles 覆盖：

- registry alias 和 helper constructor
- initialize auth method normalization
- 安全的 protocol-only agent authentication
- initial config option selector
- tool kind 到 operation kind 的 projection
- simulator compatibility

规则：

- runtime policy 名称保持精确：`yolo`、`accept-edits`、`read-only`。
- raw agent mode 与 runtime policy projection 分离。
- terminal auth 应由宿主驱动；runtime 只允许自动执行安全的 protocol-only `agent` auth。
- 依赖 `Agent.Type` 的行为归一化应放在 `profiles.go`。
