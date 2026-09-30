# 稳定 ACP 升级与可靠性迁移

目标为官方 **schema-v1.23.0 稳定文件**，wire protocol 仍为 **1**。[精确能力矩阵](../research/protocol-coverage-matrix.md)区分已实现、按 handler 启用、原有实验兼容项与明确排除项；原始 schema、hash、独立 Python 对端和真实 wire fixtures 已锁定。

## 必须注意的行为变化

- 权限采用标准嵌套 toolCall、optionId、outcome。宿主返回 selected + 请求实际提供的选项 ID，或 cancelled；旧 allow/扁平 JSON 不再有效。未安装 handler、超时、未知选项、撤销或晚授权统一 fail closed；没有 reject 选项时用 cancelled
- 托管会话在初始化、new/load/resume 未完成且没有活动 turn 时，不执行工具授权。宿主服务对象保持原实例；配置、快照和更新数据各自深拷贝
- New 必须返回非空 ID；Load/Resume 以请求 ID 为准，合法 `{}` 与只含 metadata 的响应不丢身份；pre-response 历史和配置会重放，冲突 ID 明确失败
- 初始化后，未 advertise 的可选方法不发送。空 capability 对象表示存在，nil 表示缺失。boolean 保留 false，不能变成字符串或缺省值
- Fork 是原有实验兼容 API，默认关闭，必须显式 EnableExperimentalFeatures；稳定升级不会启用 v2、alpha、notice、compaction 或其他 unstable 能力

## 取消、资源与事件

显式 CancelTurn 后，session 保持 cancelling/busy，直到 prompt 真正 terminal。Codex interrupt ack 只说明收到请求。15 秒 watchdog 到期或调用方 context 取消/超时，会废弃旧 transport；随后必须新建/恢复连接，不能把“调用方已返回”当作远端已停止，更不能自动重试可能发生的外部副作用。

Status 可见 running/cancelling/tainted/closed；Diagnostics 的 cleanupPending/cleanupError 表示待清理责任。失败清理仍由 Session.Close、Runtime.Close 或 SessionService.Close 重试，包括未能返回 Session 的启动失败。相同 provider session ID 不再覆盖其他连接资源。

正常屏障是协议 terminal 加接收缓冲区中已完整接收帧的固定序号水位及其反向请求处理完成，不是 read-idle 或 sleep。响应前文本留在原轮，已缓冲的响应后文本先单独交付，再开放下一轮。对于已知会在 terminal 后发旧轮消息的不合规 agent，可启用 RequireFreshConnectionPerTurn：每轮完成后关闭 handle，下一轮重新打开/恢复。没有 wire turn ID 时，本地 generation 本身无法识别同一连接中的任意迟到消息。

Completion 和快照是内存权威结果；Events/Updates 是有界预览，可能丢帧，不是 durable event log，更不保证 exactly-once。RPC 使用单一有界 writer；部分写入超时会使连接失败。stdio/可关闭 pipe 可中断；任意不可中断的自定义 Writer/Close 无法承诺 goroutine 一定退出。

## 启动配置与安全

Native 在 spawn/thread-start 之前解析配置。InitialConfig 显式会话选择覆盖 AgentConfig 基础值；与显式原生 Meta 冲突则拒绝。不能执行的安全设置明确报错，绝不能忽略或扩大权限。Codex 的统一 Allow/Ask/Deny 工具/路径规则拒绝启动；Deny 永不映射到 writable roots。显式允许写目录使用正确的 sandbox_workspace_write.writable_roots 层级。

Create/Load/Resume/实验 Fork 均应用 InitialConfig；不支持的 native effort/raw 选项启动前失败。全部 setter 完成后再次核对权威配置快照，防后一次设置重置前一次选择。旧 agent 仅返回空确认、缺少完整读回时，ProviderReadback 留空。ConfigApplication 目前覆盖 InitialConfig 的请求、来源、有效值、结果和可用读回，不冒称已统一审计所有 Env/Args 或 OS sandbox。

失败的 Resume/Load 只关闭本次连接，不删除原历史。配置 JSON 必须为非 null object；OpenCode 配置经验证后原子替换并保留权限。

## 稳定交互能力

Elicitation 只 advertise 已安装的 form/URL handler。表单拒绝凭证收集；URL accept 不代表外部流程完成。请求作用域绑定实际未结束的请求，session 作用域绑定原会话/turn；URL ID 在同一连接不能复用，达到有界 ID 上限后需重连。完成回调经有界 worker 投递，宿主应处理 context 取消并检查 ElicitationCompletionDrops()。

Terminal auth 是独立的宿主交互进程；退出 0 后销毁旧连接并重新 initialize，后续受认证保护的 session 操作成功才算验证。其 method ID 不发送到 authenticate，SDK 不采集密码、不保存凭证。

## Gateway 与部署边界

仍为本机单用户、纯文本 OpenAI 兼容层。保留 loopback；共享 API key、owner hash 和 agent discovery 不构成多租户授权或隔离。

TTL 是 idle TTL；活动 turn 不过期删除，竞争请求返回 409。断连会隔离旧会话。SSE 用权威完成文本补缺失后缀；若预览已有非前缀缺口，返回 stream_gap，不能少字却报成功。工具/plan 预览不混入回答。

图片、token 上限、stop 与非默认采样参数在启动前拒绝。temperature=1/top_p=1 仅为已说明的兼容 no-op，不保证 provider 参数。默认上限：总会话/启动 256、response aliases 4096、gateway 输出 8 MiB、stream write deadline 30 秒；这些不是 provider 内存/token/费用上限。

Linux 对已观察进程身份和后代执行 TERM→KILL 并确认退出；未观察到就脱离/重父化的进程仍需 cgroup/container 边界。macOS/Windows 当前仅确认所持 leader，交叉编译不等于跨平台后代清理实测。

## 验证与后续发布

应运行普通测试、race、vet、构建、harness 与两个独立 schema gate。先安装固定 Python validator，避免互操作测试在 CI 被跳过。Compatibility CLI 默认运行无凭证 contract；真实 suite 须显式选择并加 --live。

Simulator 审计真实结果为 29 PASS、3 SKIPPED（实验 fork 与两个 provider 专属权限场景），不再伪造 32/32。稳定版本工作流只读官方稳定 release 并产出比较 artifact，不自动改协议、开 PR 或合并；issue 变更另有开关。

真实模型/认证、其他平台运行、生产 canary 仍需独立授权与验证。持久化事件日志、外部副作用 ledger、多租户安全、完整 RSS/FD/进程 soak 和第三方 backend factory 均明确保留为后续设计工作。
