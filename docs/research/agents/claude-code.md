# Claude Code ACP Admission Notes

Language:
- English (default)
- [简体中文](../../../zh-CN/research/agents/claude-code.md)

## Summary

- Date: 2026-06-01
- Runtime repo: `acp-runtime-go`
- Agent id: `claude-acp`
- Local Claude Code: `claude --version` -> `2.1.121 (Claude Code)`
- ACP adapter: `npm exec --yes @agentclientprotocol/claude-agent-acp --` (unpinned, resolves latest; migrated from the deprecated `@zed-industries/claude-agent-acp@0.23.1` which was renamed and stopped producing output. Latest observed at time of writing: 0.55.0)
- Adapter SDK dependency: `@agentclientprotocol/sdk@1.1.0`
- Upstream protocol schema checked against `agent-client-protocol@schema-v1.17.0` (re-verified 2026-07-05; the `configId`/`auth` observations below were originally established against `v0.13.4` and remain accurate).
- Status: admitted for interactive session, config/mode, file, terminal, and write scenarios; session history operations have wrapper-level limitations when run against an empty newly-created session.

## Evidence

Commands run from `/Users/dev/acp-runtime-go`:

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

Observed results:

- `which claude` resolved to `/Users/dev/.n/bin/claude`.
- `claude --version` returned `2.1.121 (Claude Code)`.
- `claude --acp` is not supported by this local Claude Code build; it exits with `unknown option '--acp'`.
- The ACP wrapper starts and creates a session through the Go runtime.
- The interactive smoke returned assistant text for `session/prompt`.
- Harness cases `protocol.initialize`, `protocol.session-new`, `protocol.session-list`, `protocol.session-prompt`, `protocol.set-mode`, `protocol.set-config-option`, `protocol.session-cancel`, `scenario.multi-turn`, `scenario.read-file`, `scenario.run-command`, and `scenario.write-file` passed.
- Harness cases `protocol.session-resume`, `protocol.session-load`, and `protocol.session-fork` returned `Resource not found` when executed immediately after creating an empty session. The wrapper advertises these capabilities, but the observed implementation path resolves the session through Claude Code history.
- Harness case `protocol.plan-update` did not emit a `plan` event in this run.

## Runtime Compatibility Fixes

The admission runs exposed Go runtime compatibility gaps:

- `session/new` must encode missing MCP servers as `[]`, not `null`.
- MCP server definitions must preserve required empty arrays for `args`, `env`, and `headers`, and stdio MCP servers must not emit a `type` field.
- Claude wrapper emits `session/update.update.content` as a single content block object for message chunks; the Go runtime now accepts both a single object and an array.
- `session/set_config_option` uses `configId` in ACP `v0.13.4`; the Go runtime now sends `configId` while still accepting legacy `optionId` on decode.
- `clientCapabilities.auth` is not part of ACP `v0.13.4`; the Go runtime no longer emits it.

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
- Multi-turn prompting.
- Claude Code read, command, and write tool scenarios through wrapper tool updates.

Observed limits:

- `session/load`, `session/resume`, and `session/fork` failed with `Resource not found` when the input session had only been created and had no persisted Claude history.
- `plan` update emission was not observed.
- Host-authority `fs/*` and `terminal/*` requests are not covered by the Claude wrapper scenarios above; the observed file, command, and write checks use Claude Code tool updates.
