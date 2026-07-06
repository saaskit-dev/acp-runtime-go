[English](../../../../research/agents/claude-code.md)

# Claude Code ACP 准入记录

## 概要

- 日期：2026-06-01
- Runtime 仓库：`acp-runtime-go`
- Agent id：`claude-acp`
- 本机 Claude Code：`claude --version` -> `2.1.121 (Claude Code)`
- ACP adapter：`npm exec --yes @agentclientprotocol/claude-agent-acp --`（unpinned，自动拉取 latest；从已废弃的 `@zed-industries/claude-agent-acp@0.23.1` 迁移，旧包已改名且不再产出输出。撰写时 latest 为 0.55.0）
- Adapter SDK 依赖：`@agentclientprotocol/sdk@0.17.0`
- 上游协议 schema 已按 `agent-client-protocol@schema-v1.17.0` 核对（2026-07-05 重新验证；下方 `configId`/`auth` 观察最初基于 `v0.13.4` 建立，仍然准确）。
- 状态：已通过交互 session、config/mode、读文件、执行命令、写文件场景；session history 操作在空的新建 session 上存在 wrapper 层限制。

## 证据

在 `/Users/dev/acp-runtime-go` 下执行：

```sh
which claude
claude --version
go run ./cmd/acp-runtime claude --cwd /Users/dev/acp-runtime-go
go run ./cmd/acp-harness --type claude --case harness/cases/05-session-prompt.json --cwd /Users/dev/acp-runtime-go
go run ./cmd/acp-harness --type claude --case harness/cases/08-set-config-option.json --cwd /Users/dev/acp-runtime-go
go run ./cmd/acp-harness --type claude --case harness/cases/14-read-file.json --cwd /Users/dev/acp-runtime-go
go run ./cmd/acp-harness --type claude --case harness/cases/15-run-command.json --cwd /Users/dev/acp-runtime-go
go run ./cmd/acp-harness --type claude --case harness/cases/16-write-file.json --cwd /Users/dev/acp-runtime-go
```

观察结果：

- `which claude` 解析到 `/Users/dev/.n/bin/claude`。
- `claude --version` 返回 `2.1.121 (Claude Code)`。
- 当前本机 Claude Code 不支持 `claude --acp`，会以 `unknown option '--acp'` 退出。
- ACP wrapper 可以通过 Go runtime 启动并创建 session。
- 交互 smoke 对 `session/prompt` 返回了 assistant 文本。
- Harness cases `protocol.initialize`、`protocol.session-new`、`protocol.session-list`、`protocol.session-prompt`、`protocol.set-mode`、`protocol.set-config-option`、`protocol.session-cancel`、`scenario.multi-turn`、`scenario.read-file`、`scenario.run-command`、`scenario.write-file` 通过。
- Harness cases `protocol.session-resume`、`protocol.session-load`、`protocol.session-fork` 在空的新建 session 后立即执行时返回 `Resource not found`。wrapper 虽然宣告这些 capability，但实测实现路径会通过 Claude Code history 解析 session。
- Harness case `protocol.plan-update` 本轮未观察到 `plan` event。

## Runtime 兼容修复

本次准入暴露了以下 Go runtime 兼容缺口：

- `session/new` 缺省 MCP servers 必须编码成 `[]`，不能编码成 `null`。
- MCP server 定义中的 `args`、`env`、`headers` 必填数组字段即使为空也要保留，stdio MCP server 不应发送 `type` 字段。
- Claude wrapper 在消息 chunk 中会把 `session/update.update.content` 作为单个 content block object 发送；Go runtime 现在同时兼容单对象和数组。
- `session/set_config_option` 在 ACP `v0.13.4` 中使用 `configId`；Go runtime 现在发送 `configId`，同时在 decode 时兼容旧 `optionId`。
- `clientCapabilities.auth` 不属于 ACP `v0.13.4`；Go runtime 已停止发送该字段。

## 当前覆盖范围

已确认：

- 通过 wrapper 启动 agent。
- `initialize`。
- `session/new`。
- `session/list`。
- `session/prompt`。
- `session/set_mode`。
- `session/set_config_option`。
- `session/cancel`。
- `session/update` message chunk。
- 多轮 prompt。
- 通过 wrapper tool update 观察到 Claude Code 读文件、执行命令、写文件场景。

已观察限制：

- `session/load`、`session/resume`、`session/fork` 对只有创建记录、尚无 Claude history 的 session 返回 `Resource not found`。
- 本轮未观察到 `plan` update。
- 上述文件、命令、写入检查来自 Claude Code tool update，不等同于 host-authority `fs/*` 和 `terminal/*` 请求覆盖。
