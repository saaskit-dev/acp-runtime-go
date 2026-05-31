# Runtime CLI

[English](../../guides/runtime-sdk-demo.md)

维护中的 CLI 位于 `cmd/acp-runtime`。

```bash
make build
bin/acp-runtime simulator
bin/acp-runtime --list-agents
```

确定性 simulator：

```bash
bin/acp-simulator-agent --auth-mode none
```

仓库 case 验证：

```bash
bin/acp-harness --case harness/cases/05-session-prompt.json --simulator-bin bin/acp-simulator-agent
```
