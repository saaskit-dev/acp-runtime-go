# Claude Code ACP Admission Notes

Language:
- English (default)
- [简体中文](../../../zh-CN/research/agents/claude-code.md)

## Summary

- Date: 2026-06-01
- Runtime repo: `acp-runtime-go`
- Agent id: `claude-acp`
- Local Claude Code: `claude --version` -> `2.1.121 (Claude Code)`
- ACP adapter: `npm exec --yes @zed-industries/claude-agent-acp@0.23.1 --`
- Status: admitted for basic `session/new` + `session/prompt` smoke.

## Evidence

Commands run from `/Users/dev/acp-runtime-go`:

```sh
which claude
claude --version
go run ./cmd/acp-runtime claude --cwd /Users/dev/acp-runtime-go
go run ./cmd/acp-harness --type claude --case harness/cases/05-session-prompt.json --cwd /Users/dev/acp-runtime-go
```

Observed results:

- `which claude` resolved to `/Users/dev/.n/bin/claude`.
- `claude --version` returned `2.1.121 (Claude Code)`.
- `claude --acp` is not supported by this local Claude Code build; it exits with `unknown option '--acp'`.
- The ACP wrapper starts and creates a session through the Go runtime.
- The interactive smoke returned assistant text for `session/prompt`.
- Harness case `protocol.session-prompt` passed.

## Runtime Compatibility Fixes

The admission run exposed two compatibility gaps in the Go runtime:

- `session/new` must encode missing MCP servers as `[]`, not `null`.
- Claude wrapper emits `session/update.update.content` as a single content block object for message chunks; the Go runtime now accepts both a single object and an array.

## Current Scope

Confirmed:

- Agent launch through the wrapper.
- `initialize`.
- `session/new`.
- `session/prompt`.
- `session/update` message chunks.

Not yet covered in this admission pass:

- `session/load`.
- `session/resume`.
- `session/fork`.
- permission request behavior.
- mode/config option behavior beyond startup metadata.
- file, terminal, and write scenarios.
