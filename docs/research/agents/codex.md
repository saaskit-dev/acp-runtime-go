# Codex ACP Admission Notes

Language:
- English (default)
- [简体中文](../../../zh-CN/research/agents/codex.md)

## Summary

- Date: 2026-06-01
- Runtime repo: `acp-runtime-go`
- Agent id: `codex-acp`
- Local Codex CLI: `codex --version` -> `codex-cli 0.130.0`
- ACP adapter: `npm exec --yes @agentclientprotocol/codex-acp@1.1.0 --` (migrated from the deprecated `@zed-industries/codex-acp@0.15.0`; the old package was renamed)
- Status: admitted for interactive session, config/mode, plan updates, permission denial, file, terminal, write, and multi-turn scenarios.

## Evidence

Commands run from `/Users/dev/acp-runtime-go`:

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

Observed results:

- `which codex` resolved to `/opt/homebrew/bin/codex`.
- `codex --version` returned `codex-cli 0.130.0`.
- `codex acp` is not a supported subcommand in this local Codex CLI help output.
- `@zed-industries/codex-acp` latest npm version observed in this run was `0.15.0`.
- Harness cases `protocol.initialize`, `protocol.session-new`, `protocol.session-list`, `protocol.session-prompt`, `protocol.set-mode`, `protocol.set-config-option`, `protocol.session-cancel`, `protocol.plan-update`, `scenario.read-file`, `scenario.run-command`, `scenario.write-file`, `scenario.multi-turn`, `scenario.permission-denied-cancelled`, `scenario.permission-denied-end-turn`, and `scenario.permission-mode-denied` passed.
- Harness cases `protocol.session-resume` and `protocol.session-fork` returned JSON-RPC `Method not found`.
- Harness case `protocol.session-load` returned `Resource not found` when executed immediately after creating a new session.
- Harness case `scenario.permission-denied` returned JSON-RPC `method not found`; the more specific permission denial cases above passed.

## Runtime Compatibility Fixes

The admission run exposed one launch compatibility gap:

- `CreateCodexAgent` must start the maintained ACP wrapper, not `codex acp`. The Go runtime now uses `npm exec --yes @agentclientprotocol/codex-acp@1.1.0 --`.

## Current Scope

Confirmed:

- Agent launch through the wrapper.
- `initialize`.
- `session/new`.
- `session/list`.
- `session/prompt`.
- `session/set_mode`.
- `session/set_config_option`.
- `session/cancel`.
- `session/update` message chunks.
- `plan` update emission.
- Multi-turn prompting.
- Read file, command execution, write file, and permission denial scenarios.

Observed limits:

- `session/resume` and `session/fork` are not exposed by this adapter path.
- `session/load` did not load a newly-created empty session in this run.
- The generic `scenario.permission-denied` case hit a method mismatch, while the explicit permission-denial outcome cases passed.
