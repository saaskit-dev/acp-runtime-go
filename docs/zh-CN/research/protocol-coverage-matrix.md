# ACP 稳定协议覆盖矩阵

固定目标为官方 `schema-v1.23.0`，commit `6d08f412a7a1370d3cc9a124e3be3d6acf92641e`；wire protocol 仍为 **1**。这不代表实现所有可选能力。[英文详细说明](../../research/protocol-coverage-matrix.md)

`testdata/acp/` 保存完整官方稳定 schema、1.17 基线、SHA-256 锁、独立手写 raw JSON fixtures，以及 Python stdio peer。`jsonschema==4.26.0` 验证实际 Go 输出，测试不需要账号或收费模型调用。

| 能力 | 支持范围 |
|---|---|
| 初始化、new、prompt、cancel、update | 稳定 v1；由独立 stdio peer 与运行时回归测试覆盖 |
| permission | 标准嵌套 toolCall / optionId / outcome；保留名称、输入、内容、位置、元数据；非法选项、未知结果、超时、取消和过期作用域 fail closed |
| load / resume | 初始化后必须有对应能力；响应 `{}` 不丢请求 session ID；冲突 ID 拒绝 |
| list / close / delete / logout | 依照 advertised 对象存在性调用；空对象不丢失 |
| usage_update | 顶层 used / size / 可选 cost；零值保留，与实验性每轮 token usage 分开 |
| boolean config | 明确声明 client session.configOptions.boolean={}；仅向提供 boolean 选项的 agent 发送 type=boolean；false 不转字符串或视为缺失 |
| select config | 公开 Options / Groups 保留；wire 分组放在 options 中，使用 group/name/options |
| tool-call name | 创建与更新保留；省略和 null 不覆盖旧值 |
| elicitation form / URL | 分别安装 handler 才声明；accept/decline/cancel、超时、关闭、重复/未知完成 ID、原连接和原会话/turn 作用域隔离 |
| terminal auth | 必须安装独立交互认证 handler；只用宿主配置命令；退出0后重新连接初始化，再由受认证限制的 session 操作核验 |
| terminal 工具执行 | 与交互认证能力分开，不能代替 terminal-auth handler |
| fork | 现有实验接口；默认关闭，须显式 EnableExperimentalFeatures |
| v2、alpha、notice、compaction、unstable 新增项 | 排除，不因跟进稳定 tag 自动启用 |

## 宿主必须实现的安全交互

Elicitation 放在连接层，允许建 session 前按 requestId 交互。宿主须显示请求 agent、消息，以及拒绝/取消控件；form 提交前允许用户检查修改，不能收集密码、API key、token、恢复码、私钥或支付凭证。库会拒绝明显凭证字段和未知表单类型，但不能替代宿主对语义与用户授权的核验。

URL handler 必须显示完整 URL 与域名，获得用户同意，并使用模型无法读取页面/输入的安全外部上下文。库不会自动打开或预取 URL。accept 只表示同意打开，不代表外部流程完成；完成通知必须匹配原连接和原请求。

权限和 elicitation 默认两分钟超时，分别可通过 PermissionTimeout / Elicitation.Timeout 调整。回调须遵守取消上下文。每连接最多32个尚未退出的权限回调执行；即使调用方超时，不响应取消的回调仍占用槽位，避免无限启动 goroutine。session 取消会取消其未决交互；缺 handler 时不声明能力。

终端认证 handler 接收宿主原 command、base/extra args 加认证 args、CWD 与覆盖合并后的配置环境；宿主负责继承启动环境并展示真正交互终端。库不执行无界面登录、不保存凭证、不解析成功输出模式。terminal method ID 绝不发给 authenticate。只尝试一次，非0/无退出码/信号/取消/重连失败均失败；退出0本身不是“已经认证”的证明。

## 迁移与验证边界

- permission 的旧扁平 JSON 与 Outcome="allow" 不再接受；使用 selected+请求实际提供的 option ID，或 cancelled
- 默认拒绝只选择实际提供的 reject_once/reject_always；未知 reject 前缀或没有拒绝选项时返回 cancelled
- load/resume 的 Go 公开别名保留，内部 wire DTO 分离，身份由请求决定
- capability 空对象表示支持；nil 表示缺失；false 不是对象
- boolean 只接受 Go bool，缺失/null/数字/字符串不能替代 false
- 初始化后未声明的可选方法不会发出；实验 Fork 须明确开启
- 旧 adapter 的自由字符串配置扩展不冒称稳定 select/boolean

独立 schema/peer 测试不等于真实 Claude/Codex 或跨平台认证 UI 验证。真实 CLI 测试仍需显式启用并固定 provider 版本。raw-message observer 属于显式诊断接口，可能包含用户内容，宿主必须负责脱敏和保留策略。

完成回调在每连接独立单 worker 执行，队列上限32，接收会话/连接取消及超时。队列溢出通过 `Connection.ElicitationCompletionDrops()` 查询，不阻塞协议读取。宿主回调必须遵守取消上下文。
