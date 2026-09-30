# Stable ACP upgrade and reliability migration

This release targets the official **schema-v1.23.0** stable file and keeps wire protocol **1**. Optional capability support is described in the [protocol matrix](../research/protocol-coverage-matrix.md). The schema lock, independent peer, and raw contract fixtures are in `testdata/acp`; exact native Codex 0.153.4 schema fixtures are in `testdata/native`.

## Observable corrections

- Permission wire uses nested `toolCall`, option `optionId`, and nested `outcome`. Return `PermissionDecision{Outcome: "selected", OptionID: offeredID}` or `cancelled`. The old `allow`/flat response is invalid. Missing handlers, unknown choices, timeout and revoked authority cannot allow a tool; without an offered reject choice denial becomes `cancelled`
- A managed runtime denies tool permission during initialization/session creation, before a live turn exists. Host authority service instances are preserved, not cloned. Metadata, snapshots and update payloads have separate ownership
- `session/new` requires a nonempty returned ID. Load/resume use the requested ID, support metadata-only or `{}` responses and pre-response replay, and reject conflicting IDs. Empty IDs fail before launch
- Optional methods require actual advertised capabilities after initialize. An empty capability object is present; nil is absent. Boolean config stays a Go `bool`, including false, and requires a boolean option advertisement
- `session/fork` remains an existing experimental compatibility API, disabled unless `RuntimeOptions.EnableExperimentalFeatures` is explicitly true. Following the stable schema does not enable v2, alpha, notice, compaction or other unstable additions

## Cancellation, ownership and delivery

An explicit `CancelTurn` requests remote cancellation and retains the active turn until its prompt terminal response. An interrupt acknowledgement is not a terminal event. If the 15-second cancellation watchdog expires, the transport is discarded. Caller context cancellation/deadline also quarantines and closes the old transport before another turn can start. Open or resume a fresh handle afterward; do not retry a possibly executed external action automatically.

`Status()` distinguishes running, cancelling, tainted and closed. Diagnostics expose `cleanupPending` and any `cleanupError`; failed cleanup remains owned and retryable by `Session.Close`, `Runtime.Close`, or `SessionService.Close`, including failures before a Session could be returned. Each connection has its own internal resource identity even if durable provider session IDs repeat.

The protocol terminal response, with a fixed watermark of complete frames already received in the read buffer and their reverse requests processed, is the normal turn barrier. Pre-response text stays in the completed turn; post-response text already buffered is delivered separately before the next turn is admitted. Read-idle and arbitrary sleeps are not terminal evidence. For a known nonconforming agent that emits old turn data after terminal, set `RequireFreshConnectionPerTurn`; the handle closes after each completed turn and the host must reopen/resume a new transport. No generation number alone can identify delayed messages without a wire turn ID.

`Turn.Completion` and read models are authoritative in-memory results. `Turn.Events` and session update channels are bounded previews and can drop updates. They are not a durable event log or exactly-once delivery. Operational hooks are bounded/serialized away from protocol IO and must still return promptly.

The RPC writer is one bounded connection worker. A timed-out partial write fails the connection rather than interleaving another frame. Stdio and closable pipes are interruptible; a custom `io.Writer` whose Write/Close cannot be interrupted cannot offer a goroutine-exit guarantee. Custom factories must honor that transport contract.

## Configuration

Startup configuration is resolved before native spawn/thread creation. Explicit InitialConfig session choices override projected AgentConfig base values; conflicting explicit native metadata is rejected. Safety settings that cannot be represented are errors, not silently ignored. Codex unified Allow/Ask/Deny tool/path rules are rejected; Deny never becomes writable roots. Explicit supported writable roots belong under `sandbox_workspace_write.writable_roots`.

Create, load, resume and experimental fork apply requested InitialConfig. Native model/mode choices must be supported by the provider; unsupported effort/raw settings fail before launch. Requested options must exist and the final authoritative provider snapshot must still agree after all setters. Legacy acknowledgements without a full config snapshot are distinguishable by missing `ProviderReadback`.

`Metadata().ConfigApplication` records InitialConfig request, source, effective choice, application outcome and available provider readback. This is deliberately not a comprehensive report of every environment/argv/provider setting or OS-level sandbox enforcement. Failed resume/load only close the new connection and preserve existing durable history.

JSON configuration must be a non-null object; null, arrays, scalars and malformed JSON return errors. OpenCode file changes use validated atomic replacement and preserve existing permissions.

## Interactive capabilities

Install only the elicitation modes the host can actually display. Forms cannot collect credentials, and URL acceptance does not mean the external flow completed. Request-scoped elicitation is bound to the live outgoing request; session-scoped interactions retain their original session/turn lease. URL IDs cannot be reused on a connection; a bounded ID budget requires reconnecting after exhaustion. Completion callbacks use a bounded worker; monitor `ElicitationCompletionDrops()` and honor callback cancellation.

Terminal authentication uses a separate host-owned interactive invocation, followed by disposal and reinitialize of a new ACP connection. The authentication method ID must never be sent to `authenticate`. Exit 0 alone does not prove login: the subsequent gated session operation must succeed. The SDK does not collect passwords or save credentials.

## Gateway and deployment

The gateway remains a local, single-user, text-only compatibility layer. Keep loopback binding; a shared API key and owner hash do not establish multi-tenant authorization, an agent allowlist or filesystem isolation.

TTL is idle time. A busy session is retained and competing requests get HTTP 409 on both endpoints. Client disconnect quarantines the handle. Stream delivery repairs a missing suffix from the authoritative completion; a non-prefix gap emits `stream_gap` without a successful terminal. Tool and plan previews are not injected into assistant text.

Images, token-limit fields, stop sequences and non-default sampling are rejected before provider startup. Temperature=1 and top_p=1 are documented compatibility no-ops, not provider sampling guarantees. Default bounds are 256 total sessions/in-flight starts, 4096 response aliases, 8 MiB gateway output and a 30-second stream-write deadline. These are not provider memory, token or billing limits.

Linux cleanup validates observed process identities, escalates TERM to KILL and checks observed descendants after the leader exits. An unobserved daemonized/reparented process can escape without an external cgroup/container boundary. Non-Linux cleanup currently confirms the owned leader only; cross-compilation does not establish full descendant cleanup on macOS or Windows.

## Verification and release boundary

Run `go test ./...`, `go test -race ./...`, `go vet ./...`, `make build`, `make harness-full`, `python3 harness/validate_transcript.py harness-outputs/full.json`, and both schema fixture validators. Install the pinned Python validator before Go tests so independent interop is not skipped. The compatibility CLI defaults to credential-free contract tests; live suites require explicit selection and `--live`.

The full simulator audit reports 29 applicable passes and three explicit skips, rather than manufacturing 32 passes. The skips are experimental fork and two provider-specific permission cases. The scheduled stable-schema workflow only reads official stable release metadata and emits comparison artifacts; it does not change the protocol or auto-merge. Optional issue mutation remains separately gated.

No local result substitutes for credential-gated provider/model tests, platform runtime tests, production canary or deployment approval. Cross-restart durability, external side-effect ledgers, multi-tenant security, full runtime memory/soak budgets and third-party backend factories remain separately scoped work.
