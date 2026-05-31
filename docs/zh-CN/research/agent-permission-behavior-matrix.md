[English](../../research/agent-permission-behavior-matrix.md)

# Agent 权限行为矩阵

## 摘要

不能继续假设所有 ACP agent 都实现同一种权限交互。

即使高层用户意图相同，例如“在受限权限下尝试写文件”，不同 agent 也可能在这些方面表现不同：

- 是否发出 `session/request_permission`
- 权限被拒绝后，turn 是 `cancelled` 还是 `end_turn`
- 拒绝后是否还会发 `tool_call_update`
- 是否用 assistant 文本解释拒绝原因
- mode 是否会改变权限语义

这份矩阵只记录我们已经直接验证过的两个 agent：

- `codex-acp`
- `claude-acp`

观测日期：

- `codex-acp`：`2026-04-11`
- `claude-acp`：`2026-04-12`

## 范围

这份文档刻意收窄范围：

- 只写已经通过 ACP 流量直接验证过的 agent
- 只写权限敏感的写文件行为
- 不试图把这些行为上升成 ACP 的统一标准

## 行为家族

目前已经能明确分出几类行为：

| 家族 | 描述 | 示例 |
| --- | --- | --- |
| `permission-request -> cancelled` | agent 请求权限，宿主拒绝后 turn 直接以 `cancelled` 结束 | `codex-acp` 的 `read-only` |
| `permission-request -> end_turn + failed tool update` | agent 请求权限，宿主拒绝后工具尝试失败，但 turn 仍正常结束 | `claude-acp` 的 `default` |
| `no permission request -> deny by mode` | agent 不请求权限，由 mode 直接拒绝工具执行 | `claude-acp` 的 `dontAsk` |
| `no permission request -> execute directly` | agent 不请求权限，由特权 mode 直接执行工具 | `claude-acp` 的 `bypassPermissions` |

这些都是已观测到的真实实现，不应被强行合并成一个假标准。

## 行为矩阵

| Agent | 版本 | Mode | 是否发 `session/request_permission` | 拒绝方式 | `session/prompt.stopReason` | 拒绝后的工具事件 | 是否返回解释文本 | 观察结果 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| `codex-acp` | `0.9.5` | `read-only` | 是 | 宿主选择显式 reject option | `cancelled` | 拒绝后未观察到继续工具执行 | 只有权限请求前的简短前置文本 | turn 被立即取消 |
| `claude-acp` | `0.26.0` | `default` | 是 | 宿主选择 `reject` | `end_turn` | `tool_call` + 失败的 `tool_call_update` | 是 | turn 正常结束，并解释权限被拒绝 |
| `claude-acp` | `0.26.0` | `dontAsk` | 否 | mode 直接拒绝工具使用 | `end_turn` | 失败的 `edit`、失败的 `execute` | 是 | turn 正常结束，并解释当前 mode 拒绝了工具 |
| `claude-acp` | `0.26.0` | `bypassPermissions` | 否 | 不走拒绝分支，工具直接执行 | `end_turn` | 成功的 `edit` + `read` | 是 | 文件成功创建并验证 |

## Agent 备注

### `codex-acp`

是在显式设置 `read-only` mode 后，用直接 ACP 探针观察到的行为。

关键点：

- 权限请求是明确且结构化的
- 宿主拒绝后，turn 以 `stopReason: "cancelled"` 结束
- 没有观察到拒绝后的继续工具执行或失败工具更新
- 这一类行为属于 `permission-request -> cancelled`

影响：

- 宿主层不能假设“权限被拒绝”一定会以正常 `end_turn` 收尾

### `claude-acp`

是在声明 terminal auth capability 后，通过原始 ACP JSON-RPC 探针观察到的行为。

关键点：

- `default` mode 会在写文件前请求权限
- 权限被拒绝后，不会取消 turn，而是把写入尝试标成失败，并返回解释文本
- `dontAsk` 会抑制 permission prompt，通过 mode 直接拒绝工具执行
- `bypassPermissions` 会抑制 permission prompt，并直接执行工具

影响：

- mode 是权限契约的一部分，不只是 UI 标签
- 宿主层不能假设“没有 permission request”就意味着“没有工具尝试”

## 接入建议

做 runtime 或 harness 时，应明确遵守这些原则：

- 不要假设所有 agent 都暴露同一条权限路径
- 不要假设权限被拒绝后一定是 `stopReason: "cancelled"`
- 把“是否观察到 permission path”和“拒绝后的结果语义”分开建模
- 先按行为家族分类，再给每个 agent 挂上 mode 前置条件

## Harness 建议

权限相关覆盖至少要拆成两层：

1. 路径覆盖
   - agent 到底有没有发 `session/request_permission`
   - 在哪个 mode 下才会发

2. 结果覆盖
   - 权限被拒绝后 turn 是不是 `cancelled`
   - 是否以正常 `end_turn` 收尾并附带失败工具更新
   - 是否由 mode 直接抑制 prompt

这也是为什么单个 `permission-denied` 场景不够。

## 当前结论

对 `codex-acp` 和 `claude-acp` 来说，“permission denied”不是一个单一行为。

它实际上是一组由这些因素共同决定的行为：

- 厂商实现差异
- mode 语义
- agent 把拒绝建模成取消、失败，还是 mode 内拒绝

所以 runtime 和 harness 应该保留这些差异作为证据，而不是强行压成一个虚假的统一契约。
