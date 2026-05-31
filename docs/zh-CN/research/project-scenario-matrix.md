[English](../../../research/project-scenario-matrix.md)

# 项目场景回归矩阵

- 状态：Draft
- 日期：2026-04-03

## 1. 目的

协议覆盖只能回答“某个方法是否存在并工作”，但不能回答“我们项目真实依赖的组合流程是否可用”。

因此需要单独维护项目场景回归矩阵。

## 2. 场景矩阵的定位

这份矩阵关注的是：

- 我们实际会不会用到
- 多个协议能力组合在一起是否稳定
- 一个 agent 是否适合进入真实产品接入

它和协议覆盖矩阵的关系是：

- 协议矩阵验证单能力
- 场景矩阵验证组合流程

## 3. 场景分级

建议把场景分成三档：

- `P0`：接入门槛，必须通过
- `P1`：强相关增强场景，建议通过
- `P2`：扩展场景，可后续补

## 4. 标准场景矩阵

| 场景 ID | 级别 | 目标 | 涉及协议能力 | 说明 |
| ---- | ---- | ---- | ---- | ---- |
| `scenario.new-prompt-complete` | P0 | 新建 session 后完成一次正常 turn | `initialize` `session/new` `session/prompt` `session/update` | 最小主路径 |
| `scenario.turn-cancel` | P0 | active turn 被取消后正常收尾 | `session/prompt` `session/cancel` `session/update` | 验证取消语义 |
| `scenario.load-continue` | P0 | load 旧 session 后继续 turn | `session/load` `session/prompt` | 若 agent 宣告支持 load 则必测 |
| `scenario.mode-switch-then-prompt` | P1 | 切 mode 后继续 turn | `session/set_mode` `current_mode_update` `session/prompt` | mode 能力相关 |
| `scenario.config-switch-then-prompt` | P1 | 更新 config 后继续 turn | `session/set_config_option` `config_option_update` `session/prompt` | config 能力相关 |
| `scenario.read-file` | P0 | 读取文件并完成回答 | `session/request_permission` `fs/read_text_file` | 文件读主路径 |
| `scenario.write-file` | P0 | 写文件并验证结果 | `session/request_permission` `fs/write_text_file` | 文件写主路径 |
| `scenario.run-command` | P0 | 执行命令并读取输出 | `terminal/create` `terminal/output` `terminal/wait_for_exit` `terminal/release` | terminal 主路径 |
| `scenario.permission-denied` | P0 | 权限请求路径存在且顺序正确 | `session/request_permission` | 只验证拒绝路径，不绑定最终 stopReason |
| `scenario.permission-denied-cancelled` | P1 | 权限被拒绝后 turn 以 `cancelled` 结束 | `session/set_mode` `session/request_permission` `session/prompt` | 适用于 `codex-acp` 一类行为 |
| `scenario.permission-denied-end-turn` | P1 | 权限被拒绝后 turn 仍以 `end_turn` 结束 | `session/request_permission` `session/prompt` | 适用于 simulator 基线 |
| `scenario.permission-mode-denied` | P1 | agent 不请求权限，由 mode 直接拒绝工具执行 | `session/set_mode` `session/prompt` `tool_call_update` | 适用于 `claude-acp` 的 `dontAsk` 一类行为 |
| `scenario.non-interactive` | P1 | 无 TTY / 无交互权限条件下的行为 | permission / terminal / fs | 自动化环境关键 |
| `scenario.long-running-terminal` | P1 | 长命令执行、输出轮询、收尾 | `terminal/create` `terminal/output` `terminal/kill` `terminal/release` | terminal 完整语义 |
| `scenario.tool-call-stream` | P1 | 工具调用与更新事件流 | `tool_call` `tool_call_update` | tool reporting |
| `scenario.resume-if-supported` | P2 | session/resume 语义 | `session/resume` | 待协议与 agent 支持成熟后强化 |

## 5. 场景通过标准

每个场景至少要定义：

- 输入 prompt
- 前置条件
- 期望的协议序列
- 期望的宿主决策
- 通过标准
- 失败标准

例如 `scenario.write-file` 的通过标准至少包括：

- agent 正常请求写权限或按其能力模型完成写流程
- 目标文件状态符合预期
- turn 最终进入可解释的完成状态
- transcript 中能看到完整 request / response / event 链

## 6. 场景与协议矩阵的关系

每个场景都必须标出它依赖的协议能力。

这样当某个场景失败时，可以快速判断：

- 是单一协议能力失败
- 还是多个能力组合后失败

## 7. 新 agent 接入的最小要求

新 agent 接入时，至少要跑完所有 `P0` 场景。

如果 agent 宣告支持的 optional capability 会影响 `P1` 场景，也应同步跑完对应 `P1`。

`P2` 可以在首批接入后逐步补齐。

## 8. 输出建议

场景结果建议写入：

```json
{
  "scenarioResults": {
    "scenario.write-file": {
      "status": "PASS",
      "level": "P0",
      "protocolDependencies": [
        "session/request_permission",
        "fs/write_text_file"
      ],
      "notes": []
    }
  }
}
```

## 9. 当前结论

新 agent 是否可接入，不能只看协议能力是否存在，还必须看项目场景矩阵是否通过。

协议覆盖决定“会不会”，场景矩阵决定“能不能用”。

[English](../../research/project-scenario-matrix.md)
