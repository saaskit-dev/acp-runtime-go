# RFC-0005: Simulator Agent ACP

Language:
- English (default)
- [简体中文](../zh-CN/rfcs/0005-simulator-agent.md)

## Decision

The simulator agent is implemented in Go under `simulator/` with the executable
entry point `cmd/acp-simulator-agent`.

It is a deterministic ACP stdio agent for runtime and harness validation. It is
not the product API.

## Supported Methods

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

The simulator supports deterministic prompts for:

- describe
- plan
- read
- write
- run
- rename

Use `make harness-admission` for the first-pass simulator gate.
