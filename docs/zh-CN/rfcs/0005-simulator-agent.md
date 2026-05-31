# RFC-0005：Simulator Agent ACP

[English](../../rfcs/0005-simulator-agent.md)

## 决策

simulator agent 使用 Go 实现，代码位于 `simulator/`，可执行入口是 `cmd/acp-simulator-agent`。

它是用于 runtime 和 harness validation 的确定性 ACP stdio agent，不是产品 API。

## 支持的方法

- `initialize`
- `authenticate`
- `session/new`
- `session/list`
- `session/load`
- `session/resume`
- `session/fork`
- `session/prompt`
- `session/set_mode`
- `session/set_config_option`
- `session/close`
- `session/update`

## Prompt Actions

simulator 支持确定性 prompt：

- describe
- plan
- read
- write
- run
- rename

使用 `make harness-admission` 作为第一道 simulator gate。
