# 原生 vs ACP 传输性能基准

测试环境：macOS (arm64)、claude 2.1.215、codex 0.153.4、magpie 网关
（claude: copilot/claude-sonnet-4.6；codex: codex/gpt-5.5，responses 线）。
ACP wrapper 使用 npx 缓存中的 claude-agent-acp v0.67.0 / codex-acp v1.1.13（离线直跑 dist/index.js）。

运行方式：

```bash
MAGPIE_BASE_URL=http://127.0.0.1:3425/v1 MAGPIE_API_KEY=magpie \
CODEX_NATIVE_TEST_HOME=$PWD/.tmp/codex-home \
go test -run "TestPerf" -v -count=1 .
```

## 真实模型（magpie 网关）

| 场景 | 指标 | avg | min | max |
|---|---|---|---|---|
| **claude 原生**（sonnet-4.6） | 会话创建 | **170ms**（第 2 个 1ms） | 1ms | 340ms |
| **claude 原生** | 首轮 turn | **2.7s** | 2.1s | 3.2s |
| **claude 原生** | 温热 turn | **1.9s** | 1.8s | 1.9s |
| claude ACP wrapper v0.67 | 会话创建 | 892ms | | |
| claude ACP wrapper v0.67 | turn | 15.9s | | |
| **codex 原生**（gpt-5.5） | 会话创建·冷（daemon 拉起） | 5.3s | | |
| **codex 原生** | 会话创建·热（守护进程池化 thread/start） | 5.1s | | |
| **codex 原生** | turn | 45.9s | 45.7s | 46.2s |
| codex ACP wrapper v1.1.13 | 会话创建 | 635ms | | |
| codex ACP wrapper v1.1.13 | turn | 48.7s | | |

## 纯协议基线（无模型，隔离进程+协议开销）

| 场景 | 会话创建 | turn 往返 |
|---|---|---|
| ACP simulator（Go stdio JSON-RPC） | 6ms | <1ms |
| codex 原生（fake app-server） | 5ms | <1ms |

## 结论

1. **claude：原生全面胜出**。会话创建 170ms vs 892ms（5×），turn 1.9s vs 15.9s（**8×**）。wrapper（SDK 多一层）每 turn 开销显著。
2. **codex：turn 时间由上游模型主导**（gpt-5.5 + reasoning，45-49s），传输层差异被淹没；wrapper 会话创建更快（635ms，wrapper 进程单层启动），原生冷 daemon 5.3s。
3. **守护进程池化收益有限**：codex thread/start 本身 ~5s（模型刷新/MCP 初始化），池化省掉 daemon 拉起仅 ~0.2s。真正的收益在于同配置会话共享进程，避免进程增殖。
4. **纯协议开销**：ACP JSON-RPC 与原生协议等价（6ms vs 5ms，均为进程启动主导）——传输协议本身不是瓶颈，进程生命周期管理才是。
