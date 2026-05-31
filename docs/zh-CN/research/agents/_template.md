[English](../../../../research/agents/_template.md)

# <agent-name> 调研模板

- 状态：Draft
- 调研日期：YYYY-MM-DD
- 调研人：

## 1. 基础信息

- agent 名称：
- adapter / command：
- 版本：
- 平台：
- 是否需要额外认证：

## 2. 测试环境

- 工作目录：
- 是否有 TTY：
- 关键环境变量：
- ACP adapter 启动命令：

## 3. 协议能力

请记录真实观察到的能力：

- 是否支持 `session/load`：
- 是否支持 `session/resume`：
- 是否支持 `session/set_mode`：
- 是否支持 `session/set_model`：
- 是否会发 `session/request_permission`：
- 其他重要 capabilities：

## 4. mode 信息

- 是否有 mode：
- mode 列表来源：
- 可见 mode：
- mode 是否稳定：
- mode 是否明显承载权限语义：
- 默认 mode：

## 5. 默认行为

在不额外设置 mode / policy 的情况下：

- 读文件行为：
- 写文件行为：
- 删除文件行为：
- 运行命令行为：
- terminal 行为：
- 权限请求行为：

## 6. 权限实验结果

### 6.1 读文件

- 是否触发 permission request：
- request 内容：
- 放行结果：
- 拒绝结果：

### 6.2 搜索文件

- 是否触发 permission request：
- request 内容：
- 放行结果：
- 拒绝结果：

### 6.3 写文件

- 是否触发 permission request：
- request 内容：
- 放行结果：
- 拒绝结果：

### 6.4 删除 / 移动

- 是否触发 permission request：
- request 内容：
- 放行结果：
- 拒绝结果：

### 6.5 执行命令

- 是否触发 permission request：
- request 内容：
- 放行结果：
- 拒绝结果：

### 6.6 terminal

- 是否触发 permission request：
- request 内容：
- 放行结果：
- 拒绝结果：

## 7. 非交互行为

无 TTY 条件下：

- 读文件：
- 写文件：
- 执行命令：
- terminal：
- 如果无法弹权限框时的行为：

## 8. mode 对权限的影响

请按 mode 重跑关键实验并记录：

| mode | 读 | 写 | 删除 | 执行命令 | terminal | 备注 |
| ---- | -- | -- | ---- | -------- | -------- | ---- |
|      |    |    |      |          |          |      |

## 9. runtime 侧映射建议

### `agent-default`

- 是否支持：
- 建议映射：

### `deny`

- 是否支持：
- 建议映射：
- 是否需要 runtime handler：

### `balanced`

- 是否支持：
- 建议映射：
- 是否需要 runtime handler：

### `full-access`

- 是否支持：
- 建议映射：
- 是否需要 runtime handler：

## 10. 结论

- 推荐的 `permissionPolicy` 映射：
- 不支持的策略：
- 风险点：
- 未确认项：

## 11. 证据

- transcript 目录：
- summary 文件：
- 相关日志：
- 参考文档：

[English](../../../research/agents/_template.md)
