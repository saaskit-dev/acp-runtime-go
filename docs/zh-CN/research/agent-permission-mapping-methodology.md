[English](../../../research/agent-permission-mapping-methodology.md)

# Agent 权限映射调研方法

- 状态：Draft
- 日期：2026-04-03

## 1. 目的

这份文档定义 `acp-runtime` 后续如何为不同 agent 收集真实 ACP 数据，并据此实现权限映射与 adapter。

目标不是拍脑袋维护一份 mode 对照表，而是建立一条可重复、可验证、可扩展的数据采集链路。

注意：

- 这份文档聚焦“权限映射研究”
- 但它依赖更大的 harness 体系
- 完整覆盖范围请结合：
  - `protocol-coverage-matrix.md`
  - `project-scenario-matrix.md`
  - `agent-admission-checklist.md`

## 2. 为什么需要这份方法文档

仅靠 RFC，我们只能定义抽象：

- `permissionPolicy`
- `modeId`
- `AcpAgentPermissionAdapter`

但真正实现 adapter 时，需要知道每个 agent 的真实行为：

- 支不支持 `session/set_mode`
- 有哪些 mode
- 这些 mode 的真实权限边界是什么
- 默认行为是否已经接近 `full-access`
- 哪些能力必须靠 runtime `permissionHandler` 兜底

这些信息不能靠猜，也不能只靠零散文档。

## 3. 数据来源优先级

实现 adapter 时，数据必须按以下优先级获取。

### 3.1 协议与真实运行结果

第一优先级是 ACP 协议行为与真实 agent 运行结果。

包括：

- `initialize` 返回的 capabilities
- `session/new`
- `session/load`
- `session/resume`（若支持）
- `session/set_mode`
- `session/request_permission`
- 文件、terminal、tool 调用时的实际事件流

这是最可信的数据。

### 3.2 官方文档与 adapter 文档

第二优先级是：

- agent 官方文档
- ACP adapter 文档
- CLI 文档
- mode 说明

这类信息用于解释行为，但不能替代实测。

### 3.3 本地源码与参考实现

第三优先级是：

- agent adapter 源码
- `acpx` 源码
- 现有测试桩

这类信息可以帮助理解实现细节，但也不能代替真实集成结果。

## 4. 调研输出物

每个 agent 最终都应产出一份研究记录：

建议路径：

```text
docs/research/agents/<agent-name>.md
```

每份记录至少包括：

- agent 名称
- 版本
- adapter / command
- 测试日期
- 测试环境
- 支持的 ACP capabilities
- 可见 mode 列表
- 默认行为
- 权限相关实测结果
- 推荐的 `permissionPolicy` 映射
- 未确认项

## 5. 每个 agent 必须回答的问题

后续为任一 agent 做权限映射前，至少要回答下面这些问题。

### 5.1 基础能力

- 是否支持 `session/load`
- 是否支持 `session/resume`
- 是否支持 `session/set_mode`
- 是否支持 `session/set_model`
- 是否会发 `session/request_permission`

### 5.2 mode 能力

- 是否存在 mode 列表
- mode 列表如何获得
- mode 是否稳定
- mode 是否与权限边界强相关
- mode 是否只是行为风格，而不是权限控制

### 5.3 权限行为

- 读文件是否请求权限
- 写文件是否请求权限
- 执行命令是否请求权限
- 删除 / 移动是否请求权限
- 无 TTY 时权限行为如何
- 拒绝权限后 agent 是失败、重试、还是继续别的路径

### 5.4 默认行为

- 默认模式是否已经接近高权限
- 默认模式是否已经满足 `full-access`
- 默认模式下是否存在隐式放行

### 5.5 runtime 兜底需求

- 哪些策略可以交给 agent mode 实现
- 哪些策略必须靠 runtime handler 实现
- 哪些策略无法被该 agent 满足

## 6. 统一实验矩阵

为保证可比较性，每个 agent 至少跑同一组实验。

### 6.1 会话实验

- `initialize`
- `session/new`
- `session/load`
- `session/resume`（如果支持）
- `session/set_mode`（如果支持）

### 6.2 权限实验

在默认设置下依次触发：

- 读取文件
- 搜索文件
- 写入文件
- 修改文件
- 删除文件
- 执行 shell 命令
- 发起 terminal 会话

记录：

- 是否收到 `session/request_permission`
- 请求内容是什么
- 拒绝后会怎样
- 放行后会怎样

### 6.3 mode 实验

对于每个 mode：

- 切换到该 mode
- 重复 6.2 的权限实验
- 比较权限行为差异

### 6.4 非交互实验

在无 TTY 条件下重复关键操作，记录：

- 是否自动拒绝
- 是否自动失败
- 是否直接继续执行

## 7. 建议建立“真实 ACP 数据采集方案”

是的，后面会有很多场景需要真实 ACP 数据，我们不应该每次靠手工临时跑。

应该设计一套专门的数据采集方案。

这套方案不只服务权限映射，也服务：

- ACP 协议兼容性验证
- 项目场景回归
- 新 agent 接入门禁

## 8. 数据采集方案设计

建议建立一套 repo 内的 ACP 研究 harness，目标是快速拿到真实协议数据。

建议结构：

```text
research/
  harness/
    scenarios/
    agents/
    outputs/
```

### 8.1 Harness 要做什么

它需要支持：

- 启动指定 agent
- 建立 ACP 连接
- 跑预定义场景
- 捕获所有 JSON-RPC 消息
- 捕获关键 runtime 事件
- 输出结构化结果

### 8.2 场景库

应预置一组标准场景，例如：

- `basic-session-new`
- `basic-session-load`
- `set-mode`
- `read-file`
- `write-file`
- `delete-file`
- `run-command`
- `permission-denied`
- `non-interactive`

这样每个 agent 都跑同一批场景。

后续应再区分：

- 协议覆盖场景
- 项目场景回归场景
- 权限映射专项场景

### 8.3 输出格式

每次采集建议输出三类文件：

```text
outputs/<agent>/<timestamp>/
  transcript.jsonl
  summary.json
  notes.md
```

其中：

- `transcript.jsonl` 保存原始 ACP 消息与事件
- `summary.json` 保存结构化结论
- `notes.md` 保存人工结论和例外说明

### 8.4 transcript.jsonl 内容建议

每条至少包含：

- 时间
- 方向：inbound / outbound
- 方法名 / 通知名
- sessionId
- turnId（如有）
- 原始 payload

这能保证后续任何结论都可追溯。

## 9. 采集方案的收益

有了这套 harness，后续实现会快很多：

- 调一个 agent 的适配时，不需要重新手工摸索
- 新版本 agent 行为变化时，可以重新跑一遍场景
- RFC 里的抽象能持续被真实数据校验
- adapter mapping 可以建立在可追溯证据之上
- 新 agent 接入可以基于统一门禁做准入判断

## 10. 落地建议

建议分两步做。

### Phase 1：先有方法和模板

先建立：

- 这份 methodology 文档
- 每个 agent 的研究模板
- 输出目录规范

### Phase 2：再做 harness

再实现：

- 场景执行器
- transcript 采集器
- summary 汇总器

## 11. 当前结论

后续真实实现时，adapter mapping 的数据来源应是：

- 协议行为
- 官方文档
- 本地源码
- 我们自己的集成采集结果

其中最关键的是最后一项：我们自己采出来的真实 ACP 数据。

没有这套数据采集方案，后续权限映射和 agent 适配会长期不稳定。

[English](../../research/agent-permission-mapping-methodology.md)
