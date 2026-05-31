# Runtime CLI

Language:
- English (default)
- [简体中文](../zh-CN/guides/runtime-sdk-demo.md)

The maintained CLI lives at `cmd/acp-runtime`.

```bash
make build
bin/acp-runtime simulator
bin/acp-runtime --list-agents
```

For the deterministic simulator:

```bash
bin/acp-simulator-agent --auth-mode none
```

For repository case validation:

```bash
bin/acp-harness --case harness/cases/05-session-prompt.json --simulator-bin bin/acp-simulator-agent
```
