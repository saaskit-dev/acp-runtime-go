# Client Integration Guide

Language:
- English (default)
- [简体中文](../zh-CN/guides/client-integration-guide.md)

`acp-runtime` is now a Go SDK. Hosts integrate by constructing a `Runtime`,
resolving or providing an `Agent`, then opening a `Session`.

```go
runtime := acp.NewRuntime(acp.NewStdioConnectionFactory(acp.StdioFactoryOptions{}), acp.RuntimeOptions{})
agent := acp.CreateClaudeCodeAgent(acp.Agent{})

session, err := runtime.StartSession(ctx, acp.StartSessionOptions{
	Agent: agent,
	CWD:   cwd,
})
```

For registry-driven startup, call `ResolveRuntimeAgentFromRegistry(ctx, id)` and
pass the returned `Agent` into `StartSession`.

Authority callbacks are passed through `RuntimeOptions` or per-session handlers:

- `PermissionHandler`
- `FilesystemHandler`
- `TerminalHandler`
- `AuthenticationHandler`

The runtime owns ACP stdio startup, initialization, safe protocol authentication,
session lifecycle, update normalization, and process cleanup.

## Validation

Use deterministic simulator validation first:

```bash
make test
make harness-admission
```

Then run a real agent smoke with the runtime CLI:

```bash
make build
bin/acp-runtime claude
```
