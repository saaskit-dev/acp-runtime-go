[English](../../../../research/agents/codex.md)

# Codex ACP 准入记录

## 概要

- 日期：2026-06-01
- Runtime 仓库：`acp-runtime-go`
- Agent id：`codex-acp`
- 本机 Codex CLI：`codex --version` -> `codex-cli 0.130.0`
- ACP adapter：`npm exec --yes @agentclientprotocol/codex-acp@1.1.0 --`（从已废弃的 `@zed-industries/codex-acp@0.15.0` 迁移；旧包已改名）
- 状态：已通过交互 session、config/mode、plan update、权限拒绝、读文件、执行命令、写文件和多轮场景。

## 证据

在 `/Users/dev/acp-runtime-go` 下执行：

```sh
which codex
codex --version
npm view @zed-industries/codex-acp version bin --json
go run ./cmd/acp-harness --type codex --case harness/cases/05-session-prompt.json --cwd /Users/dev/acp-runtime-go
go run ./cmd/acp-harness --type codex --case harness/cases/08-set-config-option.json --cwd /Users/dev/acp-runtime-go
go run ./cmd/acp-harness --type codex --case harness/cases/14-read-file.json --cwd /Users/dev/acp-runtime-go
go run ./cmd/acp-harness --type codex --case harness/cases/15-run-command.json --cwd /Users/dev/acp-runtime-go
go run ./cmd/acp-harness --type codex --case harness/cases/16-write-file.json --cwd /Users/dev/acp-runtime-go
```

观察结果：

- `which codex` 解析到 `/opt/homebrew/bin/codex`。
- `codex --version` 返回 `codex-cli 0.130.0`。
- 本机 Codex CLI help 输出中没有 `codex acp` 子命令。
- 本轮观察到的 `@zed-industries/codex-acp` npm 最新版本是 `0.15.0`。
- Harness cases `protocol.initialize`、`protocol.session-new`、`protocol.session-list`、`protocol.session-prompt`、`protocol.set-mode`、`protocol.set-config-option`、`protocol.session-cancel`、`protocol.plan-update`、`scenario.read-file`、`scenario.run-command`、`scenario.write-file`、`scenario.multi-turn`、`scenario.permission-denied-cancelled`、`scenario.permission-denied-end-turn`、`scenario.permission-mode-denied` 通过。
- Harness cases `protocol.session-resume`、`protocol.session-fork` 返回 JSON-RPC `Method not found`。
- Harness case `protocol.session-load` 在新建空 session 后立即执行时返回 `Resource not found`。
- Harness case `scenario.permission-denied` 返回 JSON-RPC `method not found`；更明确的权限拒绝 outcome cases 已通过。

## Runtime 兼容修复

本次准入暴露了一个启动兼容缺口：

- `CreateCodexAgent` 必须启动维护中的 ACP wrapper，而不是 `codex acp`。Go runtime 现在使用 `npm exec --yes @agentclientprotocol/codex-acp@1.1.0 --`。

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
- `plan` update emission。
- 多轮 prompt。
- 读文件、执行命令、写文件和权限拒绝场景。

已观察限制：

- 当前 adapter 路径不暴露 `session/resume` 和 `session/fork`。
- `session/load` 本轮不能加载新建空 session。
- 通用 `scenario.permission-denied` case 命中 method mismatch，但明确的权限拒绝 outcome cases 通过。
