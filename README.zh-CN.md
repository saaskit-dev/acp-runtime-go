# acp-runtime-go

语言：
- [English](README.md)
- 简体中文（当前）

## 概览

`acp-runtime` 是 Go 实现的 ACP host-side runtime 层，用于给产品宿主提供统一的 agent 启动、session、turn、registry 解析、profile 兼容、stdio transport、read model 和 deterministic simulator 验证模型。

这个项目仍然是 ACP-focused 的薄 runtime/facade，不是脱离 ACP 的自有后端引擎。

协议对齐信息：

- ACP protocol version: `1`
- ACP source repo: `https://github.com/agentclientprotocol/agent-client-protocol`
- ACP source ref: `v0.11.4`
- Last verified against upstream docs: `2026-04-08`

## 快速开始

```go
package main

import (
	"context"
	"fmt"
	"os"

	acp "github.com/saaskit-dev/acp-runtime-go"
)

func main() {
	ctx := context.Background()
	runtime := acp.NewRuntime(acp.NewStdioConnectionFactory(acp.StdioFactoryOptions{}), acp.RuntimeOptions{})

	agent := acp.CreateClaudeCodeAgent(acp.Agent{})
	session, err := runtime.StartSession(ctx, acp.StartSessionOptions{
		Agent: agent,
		CWD:   mustGetwd(),
	})
	if err != nil {
		panic(err)
	}
	defer session.Close(ctx)

	completion, err := session.Run(ctx, "Summarize the current workspace.")
	if err != nil {
		panic(err)
	}
	fmt.Println(completion.OutputText)
}

func mustGetwd() string {
	cwd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	return cwd
}
```

## 命令

```bash
make build
make test
make lint
./run runtime simulator
./run harness --case harness/cases/05-session-prompt.json
```

构建产物位于 `bin/`：

- `bin/acp-runtime`
- `bin/acp-simulator-agent`
- `bin/acp-harness`

## 仓库结构

- 根 package：Go runtime SDK、ACP protocol types、stdio transport、registry resolution、profiles 和 session driver。
- `cmd/acp-runtime/`：维护中的交互式 runtime CLI。
- `cmd/acp-simulator-agent/`：确定性的 ACP stdio simulator agent。
- `cmd/acp-harness/`：读取仓库 case JSON 的 Go harness runner。
- `simulator/`：simulator agent 实现。
- `harness/`：Go harness package 和 JSON case 定义。
- `docs/`：公开指南、RFC、兼容性说明和研究记录。

## Runtime 概念

- `Runtime`：宿主侧入口，负责 start/load/resume/fork/list/close sessions。
- `Session`：宿主侧 session 对象，负责 turn、agent config、snapshot 和 read model。
- `SessionDriver`：内部边界，用于把 ACP session 行为归一化到 `Session` 后面。
- `AgentProfile`：按 `Agent.Type` 选择的兼容性策略。

公共 runtime policy 名称保持精确：`yolo`、`accept-edits`、`read-only`。

## Simulator

Go simulator 是确定性的 ACP stdio agent，支持：

- `initialize`、`authenticate`、`session/new`、`session/list`、`session/load`、`session/resume`、`session/fork`
- `session/prompt`、`session/update`、`session/set_mode`、`session/set_config_option`、`session/close`
- describe、plan、read、write、run、rename 等确定性 prompt action

直接运行：

```bash
./run simulator --auth-mode none
```

## 开发

```bash
make clean
make build
make lint
make test
make harness-admission
```

生成的 runtime state 默认位于 `~/.acp-runtime/`。
可用 `ACP_RUNTIME_HOME_DIR` 覆盖 home root，用 `ACP_RUNTIME_CACHE_DIR` 覆盖 cache 路径。
