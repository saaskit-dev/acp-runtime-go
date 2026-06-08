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
- ACP source ref: `v0.13.4`
- Last verified against upstream docs: `2026-06-01`

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
- `bin/acp-openai-server`
- `bin/acp-simulator-agent`
- `bin/acp-harness`

## OpenAI 兼容服务

本仓库提供一个本地 OpenAI-compatible HTTP gateway：

```bash
./run openai-server
```

主要接口：

- `GET /v1/models`
- `POST /v1/chat/completions`
- `GET /v1/acp/sessions`
- `DELETE /v1/acp/sessions/{id}`

`GET /v1/models` 返回可直接用于 OpenAI `model` 字段的模型 ID。模型 ID 可以带 ACP agent 前缀：

- `claude/sonnet`：启动 `claude` ACP agent，并设置 ACP config `model=sonnet`
- `codex/gpt-5.5`：启动 `codex` ACP agent，并设置 ACP config `model=gpt-5.5`
- `gpt-5.5`：使用 `--agents` 的第一项，并设置 ACP config `model=gpt-5.5`

开启 `--discover-models` 后，服务会探测 `--agents` 指定的 agent，读取 ACP model metadata 或 `model` config option 的可选值，立刻关闭探测 session，并按 `--model-discovery-ttl` 缓存探测结果。显式 `--models` 配置始终会保留；agent 没有暴露模型信息或探测失败时，它就是 fallback。

默认 `--agents` 是 `claude,codex`，默认开启模型探测，默认 `--cwd` 是用户 home 目录。可选的 `--agent` 可以覆盖默认 agent，但常规用法是只配置 `--agents`；它的第一项就是默认 agent。

默认没有 `X-ACP-Session-ID` 时，请求会创建临时 ACP session，完成一个 turn 后关闭。需要复用 ACP session 时，在第一次请求加 `X-ACP-Session-Mode: persistent`，服务会在响应头返回 `X-ACP-Session-ID`：

```bash
curl -i http://127.0.0.1:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -H 'X-ACP-Session-Mode: persistent' \
  -d '{"model":"claude/sonnet","messages":[{"role":"user","content":"Summarize this repository."}]}'
```

后续请求带同一个 `X-ACP-Session-ID` 会复用该 ACP session。复用 session 时默认只把最后一条 user message 作为新 turn 输入，避免把 OpenAI 客户端重放的完整 `messages` 再次塞入已有 ACP session。需要显式重放完整上下文时，传 `X-ACP-Input-Mode: replay`。

同一个 ACP session 同一时间只允许一个 turn；并发命中会返回 `session_busy`。session 按 API key/owner、agent、model、cwd 和 system prompt hash 做隔离，并由 `--session-ttl` 控制空闲过期。

`GET /v1/acp/sessions` 会返回 gateway session id 和底层 `acp_session_id`，用于确认哪些 session 是该 gateway 开启并管理的。服务退出时会关闭所有由该 gateway 登记的 managed ACP session 和它自己创建的 runtime；`SIGINT`、`SIGTERM`、TTL 定时清理以及 `DELETE /v1/acp/sessions/{id}` 都会触发对应清理，避免本地 ACP agent 进程泄露。

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

## 性能说明

默认 RPC API 接收普通 Go struct，优先保证宿主集成简洁：`Peer.Call(ctx, method, params, &result)` 和 `Peer.Notify(ctx, method, params)`。
高吞吐宿主如果已经持有编码后的 JSON，可直接使用 `Peer.CallRaw` 和 `Peer.NotifyRaw` 传入 `json.RawMessage`，避免热路径上的 interface 装箱和重复参数编码。

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
