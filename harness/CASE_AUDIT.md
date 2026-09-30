# Stable simulator case audit

Every listed assertion is evaluated against received/write-success raw messages. Request assertions require correlated successful responses. `session/update` event assertions inspect `params.update`; permission-request fields inspect the real reverse-RPC `params`; completed events inspect prompt responses. Counts, order, fields, and tool status are executable assertions, not annotations.

| Case | Simulator applicability | Required observed evidence |
| --- | --- | --- |
| protocol.initialize | required | initialize matched RPC (or notification) |
| protocol.authenticate | required | initialize matched RPC (or notification); authenticate matched RPC (or notification) |
| protocol.session-new | required | initialize matched RPC (or notification); session/new matched RPC (or notification); session/new result.sessionId = null; initialize before session/new on the same connection |
| protocol.session-list | required | session/list matched RPC (or notification) |
| protocol.session-prompt | required | session/prompt matched RPC (or notification) |
| protocol.available-commands-update | required | available_commands_update event |
| protocol.set-mode | required | session/set_mode matched RPC (or notification) |
| protocol.set-config-option | required | session/set_config_option matched RPC (or notification) |
| protocol.session-resume | required | session/resume matched RPC (or notification) |
| protocol.session-load | required | session/load matched RPC (or notification) |
| protocol.session-fork | SKIPPED: experimental | session/fork matched RPC (or notification) |
| protocol.session-cancel | required | session/cancel matched RPC (or notification); tool_call_update event; session/prompt before session/cancel on the same connection; session/prompt result.stopReason = "cancelled" |
| protocol.plan-update | required | plan event; plan update.entries.0.content nonempty; plan update.entries.0.status = "in_progress" |
| scenario.read-file | required | session/prompt matched RPC (or notification); one of (fs/read_text_file matched RPC (or notification); tool read/completed; tool search/completed); tool_call_update event |
| scenario.run-command | required | session/prompt matched RPC (or notification); one of (terminal/create matched RPC (or notification); tool execute/completed); tool_call_update event |
| scenario.write-file | required | session/prompt matched RPC (or notification); one of (fs/write_text_file matched RPC (or notification); tool edit/completed; tool execute/completed); tool_call_update event |
| scenario.permission-denied | required | session/request_permission matched RPC (or notification); session/prompt matched RPC (or notification); session/prompt before session/request_permission on the same connection; permission-request options.0.kind nonempty |
| scenario.error-recovery | required | session/prompt matched RPC (or notification) |
| scenario.multi-turn | required | session/prompt matched RPC (or notification) |
| scenario.idempotent-resume | required | session/resume matched RPC (or notification) |
| simulator.available-command-surface | required | available_commands_update event; available_commands_update update.availableCommands.0.name = "help"; available_commands_update update.availableCommands.3.name = "bash"; available_commands_update update.availableCommands.5.name = "rename" |
| simulator.rename-session | required | session_info_update event; session_info_update update.title = "Harness Runtime Investigation" |
| simulator.scenario-full-cycle | required | plan count ≥2; tool_call_update count ≥3; fs/read_text_file matched RPC (or notification); fs/write_text_file matched RPC (or notification) |
| simulator.fault-injection | required | tool_call_update count ≥3; terminal/create matched RPC (or notification) |
| scenario.permission-denied-cancelled | SKIPPED: provider-specific | session/request_permission matched RPC (or notification); session/prompt result.stopReason = "cancelled"; tool_call_update count ≤0 |
| scenario.permission-denied-end-turn | required | session/request_permission matched RPC (or notification); session/prompt result.stopReason = "end_turn"; tool_call_update count ≥1; tool_call_update update.status = "failed" |
| scenario.permission-mode-denied | SKIPPED: provider-specific | permission-request count ≤0; session/prompt result.stopReason = "end_turn"; tool_call_update count ≥1; tool_call_update update.status = "failed" |
| host.read-file | required | session/prompt matched RPC (or notification); fs/read_text_file matched RPC (or notification); tool_call_update event |
| host.run-command | required | session/prompt matched RPC (or notification); terminal/create matched RPC (or notification); tool_call_update event |
| host.write-file | required | session/prompt matched RPC (or notification); session/request_permission matched RPC (or notification); fs/write_text_file matched RPC (or notification); tool_call_update event |
| protocol.session-delete | required | session/delete matched RPC (or notification) |
| protocol.logout | required | logout matched RPC (or notification) |

All method/field/order/count failures are fatal for applicable cases. Agent-specific probes select prompts but never imply observed evidence. See `regression_test.go` for negative proof and `simulator_test.go` for all 32 manifest runs in independent subprocesses.
