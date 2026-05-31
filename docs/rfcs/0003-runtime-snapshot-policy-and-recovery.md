# RFC-0003: Runtime Snapshot, Policy, And Recovery

Language:
- English (default)
- [简体中文](../zh-CN/rfcs/0003-runtime-snapshot-policy-and-recovery.md)

## Decision

`RuntimeSnapshot` is the minimal recovery model:

- snapshot version
- resolved `Agent`
- cwd
- MCP servers
- ACP session id
- current mode id
- raw config values applied through the runtime

The current Go port keeps durable local registry support narrow. Product hosts
should persist product-owned lifecycle state and pass a resolved `Agent`, cwd,
and handlers when loading or resuming sessions.

## Policy

Runtime policy names remain exact:

- `yolo`
- `accept-edits`
- `read-only`

Raw agent modes and runtime policy projection remain separate.
