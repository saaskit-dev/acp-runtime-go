[English](../../../../research/agents/claude-code.md)

# Claude Code ACP 准入记录

## 概要

- 日期：2026-06-01
- Runtime 仓库：`acp-runtime-go`
- Agent id：`claude-acp`
- 本机 Claude Code：`claude --version` -> `2.1.121 (Claude Code)`
- ACP adapter：`npm exec --yes @zed-industries/claude-agent-acp@0.23.1 --`
- 状态：已通过基础 `session/new` + `session/prompt` smoke。

## 证据

在 `/Users/dev/acp-runtime-go` 下执行：

```sh
which claude
claude --version
go run ./cmd/acp-runtime claude --cwd /Users/dev/acp-runtime-go
go run ./cmd/acp-harness --type claude --case harness/cases/05-session-prompt.json --cwd /Users/dev/acp-runtime-go
```

观察结果：

- `which claude` 解析到 `/Users/dev/.n/bin/claude`。
- `claude --version` 返回 `2.1.121 (Claude Code)`。
- 当前本机 Claude Code 不支持 `claude --acp`，会以 `unknown option '--acp'` 退出。
- ACP wrapper 可以通过 Go runtime 启动并创建 session。
- 交互 smoke 对 `session/prompt` 返回了 assistant 文本。
- Harness case `protocol.session-prompt` 通过。

## Runtime 兼容修复

本次准入暴露了两个 Go runtime 兼容缺口：

- `session/new` 缺省 MCP servers 必须编码成 `[]`，不能编码成 `null`。
- Claude wrapper 在消息 chunk 中会把 `session/update.update.content` 作为单个 content block object 发送；Go runtime 现在同时兼容单对象和数组。

## 当前覆盖范围

已确认：

- 通过 wrapper 启动 agent。
- `initialize`。
- `session/new`。
- `session/prompt`。
- `session/update` message chunk。

本轮尚未覆盖：

- `session/load`。
- `session/resume`。
- `session/fork`。
- 权限请求行为。
- mode/config option 行为，除启动 metadata 外。
- 文件、terminal、写入场景。
