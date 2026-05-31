[English](../../../research/agent-admission-checklist.md)

# Agent 接入门禁清单

- 状态：Draft
- 日期：2026-04-03

## 1. 目的

这份清单用于规定：新 agent 接入 `acp-runtime` 之前，必须完成哪些检查、产出哪些证据、满足哪些通过条件。

## 2. 适用范围

适用于：

- 新增内置 agent adapter
- 新增第三方 ACP agent 接入
- 现有 agent 大版本升级后的重新验收

## 3. 接入前必须完成的三类检查

### 3.1 协议覆盖检查

必须依据：

- `protocol-coverage-matrix.md`

要求：

- 所有 baseline 协议能力有明确结果
- agent 宣告支持的 optional capability 全部有明确结果
- 不得存在 `MISSING` 的宣告能力项

### 3.2 项目场景回归检查

必须依据：

- `project-scenario-matrix.md`

要求：

- 所有 `P0` 场景通过
- 与该 agent 宣告能力相关的 `P1` 场景完成执行并有结论

### 3.3 研究记录检查

必须产出：

- `docs/research/agents/<agent-name>.md`

要求：

- 记录版本、环境、命令、能力、mode、权限行为
- 记录推荐的 `permissionPolicy` 映射
- 明确未确认项和风险点

## 4. 必须产出的 artifacts

每次验收至少应产出：

```text
.tmp/harness-outputs/<agent>/<timestamp>/
  transcript.jsonl
  summary.json
  notes.md
```

并且：

- `transcript.jsonl` 必须可追溯
- `summary.json` 必须含协议覆盖与场景结果
- `notes.md` 必须记录例外和人工判断

做场景门禁判断时，`matrix-summary.json` 现在应作为第一层汇总结果。
至少应直接给出：

- 所有适用的 `P0` 场景是否已通过
- 哪些必测 `P0` 场景未通过
- 已观察到哪些权限行为 family
- 还有哪些预期权限行为 family 尚未覆盖

`make harness-admission` 是确定性 simulator admission gate。真实 agent 验收时，先构建 `bin/acp-harness`，再结合目标验收范围传入 `--type <agent>` 和对应 case。命令只应在仍存在准入阻断项时返回非零退出码。
最少包括这两类阻断：

- 任一适用的 `P0` 场景失败
- 对该 agent 已适用的 permission family 场景，仍缺少对应证据

`make harness-full` 保留为更严格的本地矩阵命令；当前 Go harness 尚未实现的 case 会明确跳过。

## 5. 通过标准

一个 agent 可以进入接入实现阶段，至少需要满足：

- baseline 协议能力无阻断性失败
- 所有 `P0` 场景通过
- 所有宣告能力项无 `MISSING`
- 权限策略映射有明确研究结论
- transcript 和 summary 可追溯

## 6. 阻断条件

以下情况应直接阻断接入：

- `initialize` / `session/new` / `session/prompt` 主路径失败
- `session/cancel` 语义不稳定
- 项目 `P0` 场景失败
- agent 宣告支持某能力，但该能力测试结果为 `FAIL`
- agent 宣告支持某能力，但没有对应测试结果

## 7. 可接受的例外

以下情况可以不阻断，但必须记录：

- agent 未宣告某 optional capability，结果为 `N/A`
- `P1` / `P2` 场景暂未通过，但不影响当前接入范围
- 某些扩展能力仅在后续阶段实现

## 8. 接入后的持续验证

agent 不是一次验收后永久稳定。

因此还应在这些场景重新执行门禁：

- agent 版本升级
- ACP adapter 升级
- runtime 权限或 terminal 实现改动
- 与 session / turn / observability 相关的重大重构

## 9. 当前结论

新 agent 接入不应靠主观判断或零散手测。

应以：

- 协议覆盖矩阵
- 项目场景矩阵
- 接入门禁清单

三者共同决定是否准入。

[English](../../research/agent-admission-checklist.md)
