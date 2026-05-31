# RFC-0004: Runtime Diagnostics And Host Integration

Language:
- English (default)
- [简体中文](../zh-CN/rfcs/0004-runtime-diagnostics-and-host-integration.md)

## Decision

Go runtime diagnostics are intentionally small:

- typed `RuntimeError` values with `Kind`, `Op`, `Msg`, and `Cause`
- raw ACP message tapping through `StdioFactoryOptions.OnACPMessage`
- deterministic simulator and harness validation as behavioral evidence

Hosts should attach their own logging, tracing, and redaction policy at the
transport boundary. Prompt and tool content should not be captured without an
explicit product policy.

## Integration Boundary

Host code calls `Runtime` and `Session`. It should not talk directly to raw
JSON-RPC unless it is implementing a custom transport or protocol diagnostic
tool.
