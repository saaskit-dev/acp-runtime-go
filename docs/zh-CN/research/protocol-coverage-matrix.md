[English](../../../research/protocol-coverage-matrix.md)

# ACP 协议覆盖矩阵

- 状态：Draft
- 日期：2026-04-03

## 1. 目的

这份文档定义 `acp-runtime` 应如何覆盖 ACP 官方协议中的能力，并规定 harness 如何记录每个 agent 的协议兼容结果。

这里的目标不是要求 runtime 在第一阶段把所有 optional 特性都实现完，而是要求：

- ACP 协议能力全部进入覆盖模型
- 每个 agent 的宣告能力都必须有测试结果
- 每一项结果都必须有 transcript 证据

## 2. 结果状态

矩阵中每一项能力都应落在以下状态之一：

- `PASS`
- `FAIL`
- `N/A`
- `MISSING`

含义：

- `PASS`：agent 宣告支持，且 harness 测试通过
- `FAIL`：agent 宣告支持，但 harness 测试失败
- `N/A`：agent 未宣告该 capability，或该能力不适用于当前 agent
- `MISSING`：agent 宣告支持，但当前还没有对应测试或本次未执行

## 3. 覆盖原则

- 协议能力全部进入 matrix
- baseline 能力必须进入首批实现与首批测试
- optional 能力如果 agent 宣告支持，则必须执行对应测试
- extension 能力允许单独记录，但不能伪装成标准协议能力

## 4. 覆盖层级

矩阵中的每项能力都应同时标注：

- 协议层级：`baseline` / `optional` / `extension`
- runtime 实现状态：`planned` / `partial` / `implemented`
- harness 状态：`has-case` / `missing-case`

## 5. 协议能力矩阵

下表用于定义标准覆盖范围。

| 能力组 | 方法 / 能力 | 协议层级 | 说明 | harness 要求 |
| ---- | ---- | ---- | ---- | ---- |
| 初始化 | `initialize` | baseline | 建立能力协商入口 | 必测 |
| 认证 | `authenticate` | optional | 仅当 agent 触发认证流时测试 | 按需 |
| Session | `session/new` | baseline | 创建会话 | 必测 |
| Session | `session/load` | optional | 恢复并回放历史 | agent 宣告支持即必测 |
| Session | `session/list` | optional | 列出可装载 session | agent 宣告支持即必测 |
| Prompt | `session/prompt` | baseline | 发起 turn | 必测 |
| Prompt | `session/update` | baseline | turn 事件流与消息更新 | 必测 |
| Prompt | `session/cancel` | baseline | 取消 active turn | 必测 |
| Mode | `session/set_mode` | optional | 切换 mode | agent 宣告支持即必测 |
| Mode | `current_mode_update` | optional | agent 主动更新当前 mode | agent 宣告支持即必测 |
| Config | `session/set_config_option` | optional | 设置配置项 | agent 宣告支持即必测 |
| Config | `config_option_update` | optional | agent 主动更新配置状态 | agent 宣告支持即必测 |
| Permission | `session/request_permission` | optional | 请求宿主权限决策 | agent 使用 client authority 时必测 |
| FS | `fs/read_text_file` | optional | 读取文件 | 若 agent 使用该能力则必测 |
| FS | `fs/write_text_file` | optional | 写入文件 | 若 agent 使用该能力则必测 |
| Terminal | `terminal/create` | optional | 创建 terminal | 若 terminal capability 存在则必测 |
| Terminal | `terminal/output` | optional | 获取输出 | 若 terminal capability 存在则必测 |
| Terminal | `terminal/wait_for_exit` | optional | 等待退出 | 若 terminal capability 存在则必测 |
| Terminal | `terminal/kill` | optional | 杀死进程 | 若 terminal capability 存在则必测 |
| Terminal | `terminal/release` | optional | 释放 terminal 资源 | 若 terminal capability 存在则必测 |
| Tool Calls | `tool_call` / `tool_call_update` | baseline | 通过 `session/update` 上报工具调用状态 | 必测 |
| Extensibility | `_meta` | baseline | 附加元数据透传 | 必测兼容 |
| Extensibility | 自定义 `_` 前缀方法 | extension | 私有扩展 | 记录即可 |

## 6. 每项测试最少记录什么

每个协议能力测试，至少需要在输出中包含：

- 是否执行
- 是否通过
- 关联的 transcript 片段
- 若失败，失败阶段
- 若为 `N/A`，为什么是 `N/A`

建议写入 `summary.json` 的字段：

```json
{
  "protocolCoverage": {
    "session/load": {
      "status": "PASS",
      "advertised": true,
      "caseId": "protocol.session-load",
      "notes": []
    }
  }
}
```

## 7. transcript 关联要求

协议覆盖结论必须能回溯到 transcript。

例如：

- `initialize` 的 request / response
- `session/new` 的 request / response
- `session/update` 的通知序列
- `session/request_permission` 的请求与 decision
- `terminal/*` 的调用序列

如果某项能力没有 transcript 证据，不应标为 `PASS`。

## 8. 与 runtime 实现的关系

这份矩阵不是“当前必须全部实现完的功能列表”，而是：

- runtime 设计边界的完整协议地图
- harness 必须知道的完整覆盖面
- 新 agent 接入时必须对照的兼容清单

因此：

- 可以有 `planned`
- 可以有 `partial`
- 但不能没有矩阵位置

## 9. 当前结论

`acp-runtime` 后续应把 ACP 官方协议中我们可能涉及的能力全部纳入 coverage matrix。

是否在 v1 首批实现，不由这份矩阵决定；
但是否被纳入测试与接入门禁，由这份矩阵决定。

[English](../../research/protocol-coverage-matrix.md)
