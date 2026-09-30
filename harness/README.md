# Observed-wire harness

The stable harness records actual JSON-RPC through `Runtime.SetConnectionObserver`
and `Connection.SetRawMessageObserver`. It never derives traffic from prompts or
manufactures events to satisfy a wait. Each frame retains its raw JSON,
connection identity, monotonic observation sequence, direction, request ID,
method, session identity, response/error, and terminal status.

An outbound attempt is not evidence of delivery. `write-success` proves that the
transport accepted the complete frame, not that its peer received it. A request
assertion additionally requires a successful response with the matching ID,
connection, and opposite direction. Notifications have no response and prove only
their observed receive/write phase. Sequence assertions compare frames on the
same connection; timestamps are not used. `completed` is derived only from an
observed successful `session/prompt` response containing `stopReason`.

Prompts run asynchronously, so waits and cancellation can happen while a turn is
active. Waits have deadlines. Cancelling an already completed turn fails. The
permission-decision step configures the real permission authority; it creates no
transcript evidence. Standalone native initialize/authenticate probes require an explicit loopback
`Runner.Factory`; without it they return UNSUPPORTED rather than driving a native
CLI with the wrong transport. Session steps use Runtime routing and are observed
for both stdio and native loopback. Unknown steps/assertions return UNSUPPORTED. Missing
required evidence and cleanup failures return FAIL. Neither status becomes PASS.
A declared provider mismatch or explicitly experimental case is SKIPPED with a
reason. The default excludes `session/fork`, which is not stable ACP.

`make harness-full` writes `harness-outputs/full.json` with every case and raw evidence.
It exits 1 for failures and 2 for unsupported required steps. It does not claim
all 32 cases passed: the local simulator matrix is 29 PASS and 3 explicit SKIPPED.
The skipped cases are experimental fork and the two provider-specific denial
cases. Other agents can produce different applicability and supported results.

To independently validate the recorded wire:

```
python -m pip install jsonschema==4.26.0
python harness/validate_transcript.py harness-outputs/full.json
```

The script uses the digest-pinned official schema-v1.23.0, not Go DTO-generated
expectations. Simulator tests are an interoperability fixture, not external
provider compatibility proof. No live model calls occur in this suite.

Read fixtures live under a per-case temporary `$fixture` directory. File-write
cases use this directory too, rather than shared `/tmp` filenames or a coincidentally
existing checkout README. `$cwd` still denotes the explicitly chosen session
working directory. Terminal scenarios execute real local commands; inspect
untrusted case definitions before running them.

## 中文说明

证据仅来自真实双向 JSON-RPC 观察。发送尝试不算成功，完整写入只证明本地传输接受；
请求还须匹配同连接、相反方向和相同 ID 的成功响应。等待缺失事件会超时，错误字段、
顺序、计数和权限选项都会失败。取消必须发生在真实活动 turn 内，完成响应以后取消不算通过。

默认稳定矩阵逐项区分 PASS、FAIL、UNSUPPORTED、SKIPPED；模拟器预期为 29 项 PASS、
3 项明确 SKIPPED，不能写成 32/32。Fork 为实验能力，另外两项仅针对指定外部 provider。
缺失的必需能力不会改成成功。文件夹具独立创建并清理，不依赖当前目录碰巧有 README。
模拟器成功不代表真实 provider 已验证；完整报告含原始消息，可再次按固定官方 schema 校验。
