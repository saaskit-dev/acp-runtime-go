[English](../../../research/registry-agent-launch-catalog.md)

# ACP Registry Agent 启动接入清单

- 状态：Draft
- 日期：2026-04-03
- 来源：`https://cdn.agentclientprotocol.com/registry/v1/latest/registry.json`

## 1. 目的

这份文档根据 ACP 官方 registry 记录每个 agent 的启动分发方式，用于：

- 生成 harness agent registry 条目
- 统一“这个 agent 怎么启动”
- 避免把启动信息和 capability / permission 结论混在一起

注意：

- registry 主要提供的是 `distribution`
- 它回答的是“怎么拿到、怎么启动”
- 它不回答 capability、auth 流程细节、permission 行为、兼容性质量
- 那些仍然要靠 harness 实测

## 2. 接入字段映射规则

registry 到 harness agent registry 的基本映射：

- `distribution.binary.*.cmd` / `args`
- `distribution.npx.package` / `args`
- `distribution.uvx.package` / `args`
- `distribution.*.env`

建议统一映射成：

```json
{
  "version": 1,
  "id": "<agent-id>",
  "displayName": "<name>",
  "transport": "stdio",
  "launch": {
    "command": "<resolved command>",
    "args": ["..."],
    "env": {}
  },
  "auth": {
    "mode": "optional"
  }
}
```

说明：

- 当前 registry 不直接提供 `transport`
- 对 registry 中这些 ACP agent，默认按 stdio ACP 启动建模
- 如果后续实测发现某 agent 不是 stdio 主路径，再在 adapter / registry 中修正

## 3. 分类

registry 当前主要有三种分发方式：

- `binary`
- `npx`
- `uvx`

### 3.1 `binary`

表示需要先下载平台对应产物，再用 `cmd` + `args` 启动。

### 3.2 `npx`

表示可直接用：

```bash
npx <package> ...
```

### 3.3 `uvx`

表示可直接用：

```bash
uvx <package> ...
```

## 4. Agent 清单

下表只记录“怎么接入启动”，不记录 capability 结论。

| id | 名称 | 分发方式 | 建议启动方式 |
| ---- | ---- | ---- | ---- |
| `amp-acp` | Amp | binary | 下载后执行 `./amp-acp` |
| `auggie` | Auggie CLI | npx | `npx @augmentcode/auggie@0.21.0 --acp` |
| `autohand` | Autohand Code | npx | `npx @autohandai/autohand-acp@0.2.1` |
| `claude-acp` | Claude Agent | npx | `npm exec --yes @zed-industries/claude-agent-acp@0.23.1 --` |
| `cline` | Cline | npx | `npx cline@2.11.0 --acp` |
| `codebuddy-code` | Codebuddy Code | npx | `npx @tencent-ai/codebuddy-code@2.70.1 --acp` |
| `codex-acp` | Codex CLI | binary / npx | `npx @zed-industries/codex-acp@0.10.0` 或下载后执行 `./codex-acp` |
| `corust-agent` | Corust Agent | binary | 下载后执行 `./corust-agent-acp` |
| `crow-cli` | crow-cli | uvx | `uvx crow-cli acp` |
| `cursor` | Cursor | binary | 下载后执行 `./dist-package/cursor-agent acp` |
| `deepagents` | DeepAgents | npx | `npx deepagents-acp@0.1.7` |
| `dimcode` | DimCode | npx | `npx dimcode@0.0.20 acp` |
| `factory-droid` | Factory Droid | npx | `npx droid@0.90.0 exec --output-format acp` |
| `fast-agent` | fast-agent | uvx | `uvx fast-agent-acp==0.6.10 -x` |
| `gemini` | Gemini CLI | npx | `npx @google/gemini-cli@0.35.3 --acp` |
| `github-copilot-cli` | GitHub Copilot | npx | `npx @github/copilot@1.0.14 --acp` |
| `goose` | goose | binary | 下载后执行 `./goose acp` |
| `junie` | Junie | binary | 下载后执行 `junie --acp=true` |
| `kilo` | Kilo | binary / npx | `npx @kilocode/cli@7.1.11 acp` 或下载后执行 `./kilo acp` |
| `kimi` | Kimi CLI | binary | 下载后执行 `./kimi acp` |
| `minion-code` | Minion Code | uvx | `uvx minion-code@0.1.44 acp` |
| `mistral-vibe` | Mistral Vibe | binary | 下载后执行 `./vibe-acp` |
| `nova` | Nova | npx | `npx @compass-ai/nova@1.0.91 acp` |
| `opencode` | OpenCode | binary | 下载后执行 `./opencode acp` |
| `pi-acp` | pi ACP | npx | `npx pi-acp@0.0.24` |
| `qoder` | Qoder CLI | npx | `npx @qoder-ai/qodercli@0.1.37 --acp` |
| `qwen-code` | Qwen Code | npx | `npx @qwen-code/qwen-code@0.13.2 --acp --experimental-skills` |
| `stakpak` | Stakpak | binary | 下载后执行 `./stakpak acp` |

## 5. registry 里带 env 的条目

部分 agent 启动时还带有 registry 建议的环境变量。

### `auggie`

```json
{
  "AUGMENT_DISABLE_AUTO_UPDATE": "1"
}
```

### `factory-droid`

```json
{
  "DROID_DISABLE_AUTO_UPDATE": "true",
  "FACTORY_DROID_AUTO_UPDATE_ENABLED": "false"
}
```

这些值应进入 harness agent registry 的 `launch.env`。

## 6. 对 harness 的直接影响

这份 catalog 可以直接驱动：

- `registry.go` / `cmd/acp-harness`
- 后续“从 registry 自动生成本地 agent registry”的脚本

但它不能替代：

- 协议覆盖矩阵
- 项目场景矩阵
- 接入门禁

## 7. 当前结论

ACP registry 现在已经足够回答“每个 agent 怎么启动接入”这个问题。

因此后续做 harness agent registry 时：

- 启动信息优先来自 registry
- capability / auth / permission / scenario 结论优先来自 harness 实测

两者不能混用。

[English](../../research/registry-agent-launch-catalog.md)
