# Runtime SDK Read Models

Language:
- English (default)
- [简体中文](../zh-CN/guides/runtime-sdk-read-models.md)

The Go runtime exposes read models as immutable snapshots from `Session`.

- `ThreadEntries()` returns user and assistant-visible history entries.
- `ToolCalls()` returns normalized ACP tool call snapshots.
- `Operations()` returns host-facing action projections derived from tool calls.
- `PermissionRequests()` returns permission projections when an agent requests host approval.
- `Metadata()` returns session title, mode, config, and available command metadata.
- `Snapshot()` returns the minimal recovery model.

The current Go port keeps watcher APIs out of the first public surface. Hosts can
consume turn events from `StartTurn` for live updates and read snapshots after
each event when they need current state.
