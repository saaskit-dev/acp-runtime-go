# Agent Permission Behavior Matrix

Language:
- English (default)
- [简体中文](../zh-CN/research/agent-permission-behavior-matrix.md)

## Summary

ACP agents should not be treated as if they all implement the same permission flow.

Even when the user intent is the same, such as "write a file under restricted permissions", agents can differ on:

- whether they emit `session/request_permission`
- whether a denied permission ends the turn as `cancelled` or `end_turn`
- whether they emit `tool_call_update` after denial
- whether they explain denial via assistant text
- whether mode selection changes the permission contract

This matrix captures the currently observed behavior for the two agents we have directly verified:

- `codex-acp`
- `claude-acp`

Observed dates:

- `codex-acp`: April 11, 2026
- `claude-acp`: April 12, 2026

## Scope

This document is intentionally narrow.

- It only covers agents we verified directly through ACP traffic
- It only covers permission-sensitive write behavior
- It does not attempt to define a normative ACP standard

## Behavior Families

The observed behaviors already split into multiple families:

| Family | Description | Example |
| --- | --- | --- |
| `permission-request -> cancelled` | Agent asks for permission, host denies, turn ends as `cancelled` | `codex-acp` in `read-only` |
| `permission-request -> end_turn + failed tool update` | Agent asks for permission, host denies, tool attempt is marked failed, turn still ends normally | `claude-acp` in `default` |
| `no permission request -> deny by mode` | Agent does not ask; restricted mode causes tool attempts to fail immediately | `claude-acp` in `dontAsk` |
| `no permission request -> execute directly` | Agent does not ask; privileged mode allows execution directly | `claude-acp` in `bypassPermissions` |

These families are all valid observed implementations.

The harness and runtime should classify them, not collapse them.

## Matrix

| Agent | Version | Mode | Emits `session/request_permission` | Deny Result | `session/prompt.stopReason` | Tool Events After Denial | Explanation Text | Observed Outcome |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| `codex-acp` | `0.9.5` | `read-only` | Yes | Host selects explicit reject option | `cancelled` | None observed after denial | Minimal preamble before permission request | Turn is cancelled immediately |
| `claude-acp` | `0.26.0` | `default` | Yes | Host selects `reject` | `end_turn` | `tool_call` + failed `tool_call_update` | Yes | Turn ends normally with denial explanation |
| `claude-acp` | `0.26.0` | `dontAsk` | No | Mode denies tool use without prompting | `end_turn` | Failed `edit`, failed `execute` | Yes | Turn ends normally with mode-based refusal |
| `claude-acp` | `0.26.0` | `bypassPermissions` | No | No denial path; tool executes directly | `end_turn` | Successful `edit` + `read` | Yes | File is created and verified |

## Per-Agent Notes

### `codex-acp`

Observed in a direct ACP probe with mode explicitly set to `read-only`.

Key points:

- Permission request is explicit and structured
- Host denial leads to `stopReason: "cancelled"`
- We did not observe post-denial tool execution or failed tool updates
- This behavior fits the `permission-request -> cancelled` family

Implication:

- Hosts cannot assume that a permission denial always produces a normal `end_turn`

### `claude-acp`

Observed in raw ACP JSON-RPC probes with terminal auth capability advertised.

Key points:

- `default` mode requests permission before writing
- Denial does not cancel the turn; the write is surfaced as a failed tool attempt and the agent returns explanatory text
- `dontAsk` suppresses permission prompts and denies restricted tool use via mode semantics
- `bypassPermissions` suppresses permission prompts and executes successfully

Implication:

- Mode is part of the permission contract, not just a UI label
- Hosts cannot assume that "no permission request" means "no tool attempt"

## Integration Guidance

When building runtime or harness behavior around ACP permissions:

- Do not assume all agents expose the same permission pathway
- Do not assume denied permissions imply `stopReason: "cancelled"`
- Separate "permission path observed" from "denial outcome observed"
- Model permission behavior by family, then attach agent-specific mode preconditions

## Harness Guidance

Permission-sensitive coverage should stay split into at least two layers:

1. Path coverage
   - Did the agent emit `session/request_permission` at all?
   - Under which mode?

2. Outcome coverage
   - After denial, was the turn `cancelled`?
   - Did the turn end normally with failed tool updates?
   - Did the mode suppress prompting entirely?

This is why a single generic `permission-denied` scenario is not enough.

## Current Conclusion

For `codex-acp` and `claude-acp`, "permission denied" is not one behavior.

It is a family of behaviors shaped by:

- vendor implementation
- mode semantics
- whether the agent treats denial as cancellation, failure, or non-prompted refusal

The runtime and harness should preserve these differences as evidence, not flatten them into a fake universal contract.
