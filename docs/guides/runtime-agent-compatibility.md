# Runtime Agent Compatibility

Language:
- English (default)
- [简体中文](../zh-CN/guides/runtime-agent-compatibility.md)

Agent-specific behavior belongs in `AgentProfile` or registry resolution, not in
host integrations.

The current Go profiles cover:

- registry aliases and helper constructors
- initialize auth method normalization
- safe protocol-only agent authentication
- initial config option selectors
- tool kind to operation kind projection
- simulator compatibility

Rules:

- Keep runtime policy names exact: `yolo`, `accept-edits`, `read-only`.
- Keep raw agent modes separate from runtime policy projection.
- Terminal auth should be host-driven; the runtime may only auto-run safe
  protocol-only `agent` auth.
- Any behavior normalization that depends on `Agent.Type` belongs in
  `profiles.go`.
