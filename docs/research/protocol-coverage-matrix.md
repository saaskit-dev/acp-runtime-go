# ACP stable protocol coverage

Target: official `schema-v1.23.0`, commit `6d08f412a7a1370d3cc9a124e3be3d6acf92641e`; wire protocol **1**. This is a pinned stable contract, not a promise to implement every optional agent feature. [中文](../zh-CN/research/protocol-coverage-matrix.md)

The immutable upstream schema, SHA-256 lock, baseline 1.17 schema, raw fixtures, and independent Python peer live in `testdata/acp/`. The independent validator uses `jsonschema==4.26.0`. It validates actual Go messages and responses, not only generated fixtures. No provider account or paid call is needed.

| Surface | Status / activation | Deterministic evidence |
|---|---|---|
| initialize, new, prompt, cancel, update | Stable v1 | independent stdio peer; runtime/session regression tests |
| permission | Stable nested toolCall / optionId / nested selected or cancelled | independent Python validation, complete approval context, malformed/unknown/timeout/cancel/late lease negatives |
| load / resume | Optional; advertised capability required after initialize | independent `{}` responses retain requested identity; conflicting IDs rejected |
| list / close / delete / logout | Optional; presence of advertised object required | empty-object preservation and no-send negative tests |
| usage_update | Stable top-level used / size / optional cost | typed decode, missing/null/negative tests, read-model ContextUsage |
| boolean config | Client advertises session.configOptions.boolean = {}; agent must offer boolean option | false retained; typed request; missing/null/string/number rejected; independent peer |
| select config | Existing public Options and Groups map onto stable options union | grouped options use group, name, options; boolean remains a distinct value type |
| tool-call name | Stable optional programmatic name | creation/update propagation; omitted or null preserves prior name |
| elicitation form | Optional host `AuthorityHandlers.Elicitation.Form` | primitive schema, enum/constraints, accept/decline/cancel, unsupported/credential schema rejection |
| elicitation URL | Optional host `AuthorityHandlers.Elicitation.URL` | explicit consent handler; completion per connection and captured scope lease; duplicate/unknown/late IDs ignored |
| terminal authentication | Optional `RuntimeOptions.TerminalAuthenticationHandler` | configured invocation, failure/cancellation tests; independent peer enforces reconnect and rejects authenticate misuse |
| ordinary terminal tools | Separate TerminalHandler | does not imply interactive authentication support |
| fork | Existing experimental compatibility API, default off | explicit `EnableExperimentalFeatures` required |
| per-turn usage, model/message extensions | Existing compatibility fields | not stable v1.23 claims; kept separately from ContextUsage |
| v2, alpha, notice, compaction, unstable additions | Excluded | no opt-in by following a stable tag |

## Host authority responsibilities

Elicitation is connection-level and can occur before a session exists. Each installed handler is a capability promise: it must identify the requesting agent, show the message, and offer decline/cancel. A form must allow review/edit before submission and must never collect passwords, API keys, payment credentials, tokens, recovery codes, or private keys. The runtime defensively rejects credential-labelled fields and unknown property types; the host still must review semantic intent. For sensitive workflows use an out-of-band URL flow instead.

URL acceptance means permission to navigate, not completion or authentication. Show the complete URL and domain, obtain consent, and use a secure context which the model cannot inspect. The runtime never opens or prefetches URLs. Completion must arrive on the original connection and is associated with the captured original request, never a later turn.

Permission and elicitation handlers default to a two-minute timeout. Set `AuthorityHandlers.PermissionTimeout` or `Elicitation.Timeout` as needed. Callbacks must honor context cancellation. At most 32 callback executions can remain in flight per connection; a slot is retained until its callback exits even if the caller timed out. Session cancellation cancels its outstanding interactions. Optional capability fields are absent without installed handlers.

The interactive auth handler receives the host-configured command, base and extra arguments followed by the method arguments, host CWD, and merged configured environment with method overrides. It must preserve inherited environment when launching and expose an actual interactive terminal. The SDK does not execute a headless login, save credentials, inspect output for success patterns, or infer success from a terminal ID. Exit 0 is followed by disposing the old connection and a fresh initialize; the subsequent authentication-gated session operation is the final check. A failed reconnect or gated operation remains a failure. There is no automatic repeated login.

## Compatibility and migration

- Public PermissionRequest/Decision stay ergonomic Go values; their JSON is now the standard nested wire. Legacy flattened permission JSON and `Outcome: "allow"` are rejected. Return `selected` with an offered option ID, or `cancelled`
- Default denial selects only offered `reject_once` / `reject_always`; absent or unknown reject kinds produce `cancelled`
- Load/resume public response aliases remain source-compatible, but wire decoding is method-specific and normalizes identity from the request
- Empty capability maps mean advertised support. Nil maps mean absent. False is not a capability object
- A boolean value is a Go bool, including false; no string coercion or missing-value default applies
- Unadvertised optional methods are rejected locally after initialize. Existing custom factory/pre-initialize low-level use must still arrange negotiation itself
- Existing fork callers must explicitly enable the experimental flag; no unstable/v2 protocol is enabled implicitly
- Arbitrary extension config types retained for older adapters are not stable select/boolean contracts and are not evidence of upstream schema coverage

## Verification limits

Passing the fixture/schema and independent-peer gates demonstrates deterministic protocol compatibility. It does not prove live Claude/Codex compatibility, desktop UI quality, user authentication, cross-platform process cleanup, or production authorization policy. Live tests remain opt-in and require separately pinned provider versions. Raw-message observers are opt-in diagnostics and may contain user content; hosts must apply their own retention/redaction policy.

Completion callbacks run on one separate worker per connection with a 32-event queue. They receive session/connection cancellation and timeout. Overflow is observable via `Connection.ElicitationCompletionDrops()`; it never blocks the protocol reader. Handlers must honor cancellation.
