# 客户端接入指南

[English](../../guides/client-integration-guide.md)

`acp-runtime` 现在是 Go SDK。宿主先创建 `Runtime`，解析或提供 `Agent`，再打开 `Session`。

```go
runtime := acp.NewRuntime(acp.NewStdioConnectionFactory(acp.StdioFactoryOptions{}), acp.RuntimeOptions{})
agent := acp.CreateClaudeCodeAgent(acp.Agent{})

session, err := runtime.StartSession(ctx, acp.StartSessionOptions{
	Agent: agent,
	CWD:   cwd,
})
```

基于 registry 启动时，调用 `ResolveRuntimeAgentFromRegistry(ctx, id)`，再把返回的 `Agent` 传给 `StartSession`。

Authority callback 可通过 `RuntimeOptions` 或单次 session options 传入：

- `PermissionHandler`
- `FilesystemHandler`
- `TerminalHandler`
- `AuthenticationHandler`

Runtime 负责 ACP stdio 启动、initialize、安全的 protocol-only authentication、session lifecycle、update normalization 和进程清理。

## 验证

先跑确定性的 simulator 验证：

```bash
make test
make harness-admission
```

再跑真实 agent smoke：

```bash
make build
bin/acp-runtime claude
```
