# RFC-0006: Native Engine Adapters (Claude / Codex direct transport)

Language:
- English (default)

## Summary

Today every agent is reached through an external ACP adapter process
(`@agentclientprotocol/claude-agent-acp`, `@agentclientprotocol/codex-acp`,
`gemini --experimental-acp`, ...). This RFC adds an alternative **native
transport** for Claude Code and Codex: the runtime drives the CLI's own
headless protocol directly, in-process, without an npm wrapper - the approach
Cumora's BYOA daemon uses - while keeping the public facade
(`Runtime`, `Session`, `StartSessionOptions`, read models, harness,
OpenAI-compatible gateway) unchanged.

## Motivation

- **Remove wrapper drift**: the npx wrapper packages resolve latest at spawn;
  upstream changes surface as runtime failures we do not control.
- **Remove npm from the hot path**: `npm exec --yes` pays npm resolution +
  cold start on every agent spawn; `claude` / `codex` are already native
  binaries on the user's machine (see `exec_path.go` PATH resolution).
- **Native-only capabilities**: mid-turn steering, native thinking streams,
  and richer usage accounting (per-hop) are visible on the native protocols
  before they appear through the ACP wrappers.
- **Windows reliability**: spawning `claude`/`codex` binaries avoids
  `.cmd` shim resolution entirely.

Non-goals: replacing ACP as the default long-tail integration. Gemini,
Copilot, OpenCode, pi and registry-discovered agents stay on ACP wrappers.
(Cumora itself uses ACP bridges for grok/zcode - ACP remains the right
answer for engines without a stable native headless surface.)

## Decision

### Insertion point: ConnectionFactory, not SessionDriver

The only seam a host configures is `ConnectionFactory` (connection.go).
`SessionService` and `acpSessionDriver` consume a `*Connection` and build
all turn state, read models, orphan-update handling, and permission
observation on top of it. Implementing native engines as direct
`SessionDriver` implementations would duplicate the entire read-model
machinery.

Instead, native adapters are alternate factories that spawn the real CLI and
**emulate the ACP agent surface in-process**:

```
acpSessionDriver --ACP JSON-RPC--> Peer --loopback pipes--> nativeBridge (agent role)
                                                                | native protocol
                                                                v
                                claude -p --input-format stream-json ...
                                codex app-server --listen stdio://
```

- The host side reuses `Peer` + `Connection` unchanged - same wire
  encoding, same `OnACPMessage` tap, same tests.
- The bridge registers the ACP agent-role methods (`initialize`,
  `session/new`, `session/prompt`, `session/cancel`, ...) and translates
  them to the native protocol; native events are translated back into
  `session/update` notifications and `session/request_permission`
  requests.
- The synthesized `initialize` response advertises **only honestly
  supported capabilities**, so hosts see reduced capability sets instead of
  silent degradation.

### Agent identity and selection

- New registry IDs: `claude-native`, `codex-native`
  (aliases `claude-code-native`, `codex-cli`). Existing `claude-acp` /
  `codex-acp` remain the default.
- `CreateClaudeCodeNativeAgent` / `CreateCodexNativeAgent` mirror the
  existing constructors; spawn is the bare binary (`claude`, `codex`),
  resolved through the existing GUI-PATH logic.
- An optional `TransportFallback bool` (StartSessionOptions or
  RuntimeOptions) lets hosts request automatic downgrade to the ACP wrapper
  when the native probe fails. The transport actually used is recorded in
  `RuntimeSessionMetadata` and a diagnostics event is emitted on fallback.

### Profile reuse

`AgentProfile` projections stay the single place where unified semantics are
translated:

- Claude native: `ProjectSystemPrompt` writes a temp file consumed by
  `--append-system-prompt-file`; `applyClaudeAgentConfig` permissions map
  to a `--settings` JSON file (`permissions.allow/deny/ask`).
- Codex native: `CODEX_CONFIG` env projection is unchanged - the native CLI
  reads the same env the wrapper did.

## Protocol mapping

### Codex (app-server JSON-RPC over stdio)

Field names must be verified against the installed CLI version at probe time
(this is a private protocol; see Risks).

| ACP | Codex app-server |
|---|---|
| `initialize` | `initialize` (clientInfo) then `initialized` |
| `session/new` | `thread/start` {cwd, model, ...}; `thread.id` becomes the ACP session id |
| `session/resume` / `session/load` | `thread/resume` {threadId}; on stale-id failure, start a fresh thread and emit a diagnostics event (Cumora pattern) |
| `session/prompt` | `turn/start` {threadId, input:[{type:"text",text}]}; resolve on turn completion |
| `session/cancel` | turn interrupt method (verify name per version) |
| agent text deltas | `session/update` agent_message_chunk |
| command / MCP / tool events | `session/update` tool_call / tool_call_update |
| exec approval request | `session/request_permission` (approve-for-turn / once / deny) - only when approval policy is not bypassed |
| usage deltas | TurnResult.Usage; optional per-hop ledger |
| model config options | static catalog (Cumora keeps model-catalog.ts for the same reason) |

One-shot fallback: `codex exec -` (prompt via stdin) - note it cannot attach
a thread id, so fallback loses continuity; surface that in diagnostics.

### Claude Code (headless stream-json)

Spawn: `claude -p --input-format stream-json --output-format stream-json --verbose [--mcp-config f --strict-mcp-config]`.

| ACP | Claude stream-json |
|---|---|
| `initialize` | no wire call; synthesized capabilities |
| `session/new` | await the `system` init event; `session_id` becomes the ACP session id |
| `session/prompt` | write one `type:"user"` NDJSON line; resolve on the `result` event (is_error -> stopReason, usage -> Usage) |
| assistant text / thinking blocks | agent_message_chunk / agent_thought_chunk |
| `tool_use` block + matching `tool_result` | tool_call begin / tool_call_update completed |
| `session/cancel` | control_request interrupt |
| permission prompt | control_request `can_use_tool` <-> `session/request_permission` (SDK control envelope; verify per CLI version) |
| `session/load` / `session/resume` / crash recovery | respawn with `--resume <session_id>` (Cumora respawn-on-crash pattern) |

## Status

- P1 (loopback bridge + Codex adapter): **landed**. `claude-native` /
  `codex-native` registry ids, `CreateCodexNativeAgent`, the in-process ACP
  bridge over loopback pipes, and the codex app-server adapter — verified
  against codex-cli 0.153.4 with a fake-server E2E plus the opt-in live test
  (`ACP_LIVE_CODEX=1`).
- P2 (Claude stream-json adapter): **landed**. Lazy spawn in session/new so
  system-prompt/model/permission/MCP metadata translates to spawn flags;
  can_use_tool control requests map onto ACP session/request_permission;
  system/init is lazy (arrives after the first user message) so the bridge
  returns a synthetic ACP session id and records claude's real uuid for
  later resume work. Verified with a fake-server E2E plus the opt-in live
  test (`ACP_LIVE_CLAUDE=1`).
- Follow-ups landed: native session/load + session/resume (claude `--resume`
  respawn; codex `thread/resume`, stale ids surface verbatim), probe +
  version floors (`NativeEngineVerifiedVersions`, `ProbeNativeEngineVersion`,
  below-floor warnings via `Session.Diagnostics()`), native engines in
  `cmd/acp-compat-check` (local `--version` + sentinel smoke, cached), and
  `TestTransportsParity` (`ACP_LIVE_PARITY=1`) running the same sentinel
  through both transports and diffing the read-model contract.

## Rollout

1. **P0 - golden streams + parity harness (about 2 days).** Record real
   claude / codex native streams into testdata; add harness cases that run
   the same script through both transports and diff thread entries, tool
   calls, usage. Reuses `cmd/acp-harness` and `cmd/acp-compat-check`;
   extend compat-check with a `--native` wake-path probe (spawn ->
   handshake -> first turn) and cache the verdict.
2. **P1 - loopback bridge + Codex adapter (about 1 week).** Smallest mapping
   surface; `CODEX_CONFIG` story unchanged; `bin/acp-openai-server` model
   discovery reads the static catalog.
3. **P2 - Claude adapter (about 1-2 weeks).** stream-json event mapping,
   control protocol for permission/interrupt, resume/respawn.
4. **P3 - selection policy.** Version floors + `blockedReason` strings,
   `TransportFallback`, startup-latency benchmark (npx exec vs binary)
   added to `session_benchmark_test.go`, docs.

## Risks

- **Private protocols.** `codex app-server` is undocumented; the Claude
  control envelope is SDK-coupled. Mitigations: minimum-version gating, a
  wake-path probe before selection, golden-file conformance tests, ACP
  wrapper fallback as the escape hatch.
- **Capability honesty.** Anything the bridge does not translate must be
  absent from the synthesized `initialize` response, not approximated.
- **Double maintenance.** Confined to two engines; everything else stays on
  ACP wrappers and the registry.

## Also adopted from Cumora (transport-adjacent)

- wake-path `doctor` probe per engine (P0).
- minimum-version check with a human-readable `blockedReason` surfaced in
  metadata (P3).
- crash -> `--resume`/`thread/resume` auto-recovery (P2).
- per-hop usage ledger for the OpenAI gateway cost view (optional).
