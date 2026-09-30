# 稳定 ACP 升级执行验收

日期：2026-09-30。基线 `fd3c9357d3a5e2094dd796b481023a2f712ea3ec`；本地分支 `fix/stable-acp-1.23-reliability`。目标为官方稳定 `schema-v1.23.0`，wire protocol 1。没有 push、PR、merge 或部署；没有真实模型、登录或费用型调用。

## 本地验收结果

- `go test -json ./... -count=1`：607 PASS / 7 SKIP / 0 FAIL（计数包含子测试）
- `go test -race -json ./... -count=1`：607 PASS / 7 SKIP / 0 FAIL
- vet、全部包构建、四个命令的 make build：PASS
- 四项有界 fuzz（每项 3 秒、两个 worker）：RPC、permission、config、load 均 PASS
- 独立 ACP schema gate：13 正例、11 负例；实际 Go↔Python stdio wire 和 terminal auth 重连测试 PASS
- 固定 Codex 0.153.4 schema gate：9 正例、3 负例、10 文件摘要校验 PASS
- 全部 32 个 harness manifests：29 PASS / 3 明确 SKIPPED；本次最终审计 543 个真实成功收发 frame 符合稳定 schema
- Compatibility CLI 默认无凭证 contract：PASS，exit 0，requiredComplete=true；wrapper/native live suites 明确 SKIPPED
- darwin/arm64、windows/amd64、linux/arm64：全部包和测试二进制交叉编译 PASS，仅编译，不冒称这些平台已实跑
- 固定稳定 Codex 0.153.4：空 HOME/CODEX_HOME 下实际 schema 生成、initialize/config-read 通过；没有 prompt/model 请求

7 个 Go skip：1 个 Windows-only 命令解析测试；6 个需明确启用的真实 Claude/Codex、网关、性能或双 transport parity 测试。Harness 跳过：实验 fork、两个 provider 专属权限场景。预装 alpha CLI 的观察未作为任何稳定验收依据。

## B01–B20 修复清单

| 项目 | 实施状态 | 主要回归证据 |
|---|---|---|
| B01 真实 harness 证据 | 已修复 | 无 agent/空断言/错字段/顺序/次数/ID/仅 attempted/跨 session 等负例；CASE_AUDIT |
| B02 权限 wire | 已修复 | 官方 raw fixture、嵌套 outcome、默认拒绝、未知选项/超时/撤销、独立 Python |
| B03 load/resume 身份 | 已修复 | `{}` 响应、冲突 ID、空 New ID、pre-response 历史与配置 replay |
| B04 send/close 竞争 | 已修复 | 20,000 次交错回归、满 buffer、阻塞 hook、全量 race |
| B05 取消与 turn 隔离 | 已修复并声明协议边界 | quarantine、watchdog、固定已接收帧水位、晚回调/同 ID 新连接、显式 fresh-connection 策略 |
| B06 Codex failed-only | 已修复 | completed/failed/interrupted/unknown、重复与错序终态 |
| B07 Codex interrupt ID | 已修复 | 真实 turn ID、request/ack、cancel-before-ID、ack 不释放 busy |
| B08 Deny 扩权 | 已修复 | ACP/native 在 factory 前拒绝不支持规则；不生成允许写目录 |
| B09 Native 启动配置 | 已修复 | model 在 thread/start 前解析，实际 HTTP 四条入口 fake provider 验证 |
| B10 Claude resume 配置 | 已修复 | pre-spawn argv、恢复 ID、失败不删原历史 |
| B11 TTL 数据竞争 | 已修复 | 旧版 expiresAt race 复现；最终 gateway race |
| B12 busy 被 TTL 删除 | 已修复 | 活动 turn 跨 TTL、第二请求 409、idle 后到期 |
| B13 busy 错回 502 | 已修复 | chat/responses、stream/nonstream、一致错误分类 |
| B14 阻塞写 deadline | 已修复 | 单 writer、有界队列、部分写隔离、短写、关闭可中断 transport |
| B15 资源身份 | 已修复 | 同 provider ID 多连接、失败启动清理记录、失败 Close 重试、关闭竞争 |
| B16 深拷贝 | 已修复 | nested map/slice/raw、循环/类型/nil、orphan update、保留活 host service 实例 |
| B17 null 配置 panic | 已修复 | 三路径非对象 JSON、非序列化 Extra、原文件/权限保留 |
| B18 suite 闭环 | 已修复 | wrapper-only cached exit0、required native 缺失 exit2、真实 FAIL exit1 |
| B19 cache 不可更新 | 已修复 | 唯一 save key、受限 restore prefix、连续运行离线模型 |
| B20 cache 身份 | 已修复 | runtime/schema/fixture/config/engine/platform/binary 摘要、TTL、旧格式 miss、脱敏诊断 |

## U00–U05 稳定升级

- U00：原始 1.17/1.23 schema、官方 tag/commit/hash/source lock 与独立 fixtures 已固定
- U01：permission/load-resume、usage_update used/size/cost、capability 空对象和 method gating 已实现
- U02：boolean false、typed set、config readback、tool-call name 与 select groups 已贯通；native 不支持的配置明确拒绝
- U03：form/URL、scope 与生命周期、超时/取消、凭证拒绝、唯一 URL ID、完成回调有界队列已实现
- U04：独立 terminal-auth invocation、退出后新连接、禁止 terminal ID 走 authenticate；受保护 session 操作验证路径已覆盖
- U05：独立语言真实 wire、固定 stable CLI 本地无凭证合约、精确矩阵与 CI/只读稳定发布监测已实现。真实模型与真实用户认证仍是单独发布门禁

## D01–D12 明确结论

- D01：慢 SSE 以权威输出修复缺失后缀；非前缀 gap 显式失败，实测 old baseline 会少字却成功
- D02：native 审批完整上下文、异步有界、取消/EOF、重复 ID token 与默认拒绝已覆盖
- D03：kind 在边界统一，unknown 不再冒称 MCP tool
- D04：Linux 已观察后代的身份/TERM→KILL/退出确认及重试通过。未观察的脱离后代需外部隔离；其他平台目前 leader-only，不能宣称完整进程树 containment
- D05：不支持的图像/token/stop/采样语义明确拒绝；兼容 no-op 已文档化
- D06：仍是本地单用户。多租户 principal/allowlist/OS sandbox、对公网部署另需安全设计和授权
- D07：增加队列、审批、replay、gateway session/start/alias/output/write-timeout 边界；完整 runtime 文本字节预算和长期 RSS/FD/进程 soak 尚未实现
- D08：InitialConfig 应用报告和最终读回已实现；所有 Env/Args/Meta/provider 来源的综合报告仍属后续增强
- D09：本次固定 ACP/native 合约和缓存身份；完整生产 wrapper/CLI/依赖 bundle 签名、升级/回滚供应链尚未实施
- D10：内存权威结果/预览已区分；跨重启 durable log、ACK/lease fencing/exactly-once 不在本次实现中
- D11：外部副作用 durable ledger 未实现，不自动重试不确定副作用
- D12：第三方 native backend factory 仍为后续 RFC，没有为本次升级重写扩展架构

## 迁移与复核

详见 [中文迁移说明](../zh-CN/guides/stable-upgrade-migration.md)、[英文迁移说明](../guides/stable-upgrade-migration.md)和[协议覆盖矩阵](protocol-coverage-matrix.md)。终态响应之后、接收 buffer 已有的帧先单独处理完再开放下一轮；未来任意时刻才到达的非合规旧消息仍需要 `RequireFreshConnectionPerTurn`，不能由本地 generation 推断来源。

本地提交按 fixtures → 配置安全 → RPC → runtime/native/wire → gateway → harness → compatibility/CI → 文档分批保留。配置、RPC、核心 runtime 的独立 index 导出快照均跑过普通全量测试；导出快照无 Git 元数据时仅关闭 VCS build stamping，未屏蔽断言。最终集成代码跑过上述完整门禁。原始失败/修复成功日志保存在交付证据包；不把中途或旧版本日志冒充最终 PASS。
