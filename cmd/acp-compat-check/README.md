# Compatibility suites

The default invocation is credential-free:

```
go run ./cmd/acp-compat-check
```

It runs the source checkout's runtime, harness and simulator contract tests. Go, Git, Python 3 and `jsonschema` must be available
(the CI-pinned validator is `jsonschema==4.26.0`). Test-result caching is disabled
and the contract run has explicit test/process deadlines. Live provider-test opt-in variables and API keys are
removed from the child test environment. CI additionally runs all packages,
race tests, the observed-wire audit and the pinned official schema gate.

Live suites must be selected and explicitly enabled:

```
acp-compat-check --suites wrapper --required wrapper --live
acp-compat-check --suites wrapper,native --required wrapper --live
acp-compat-check --suites native --required native --live --refresh
```

Wrapper smoke tests use a temporary working directory and isolated configuration
home with explicit environment credentials. Unprojected `CODEX_CONFIG` overrides
disable caching rather than hashing potentially secret configuration.

Live calls may incur provider charges. Native checks require installed and
already authenticated CLIs. Missing credentials, disabled live execution or
missing CLIs produce SKIPPED; infrastructure/auth/billing/network failures produce
INFRA_ERROR. A typed protocol violation or failed expected output produces FAIL.
Unknown errors stay FAIL. Legacy provider text diagnostics are only a fallback
following typed errors, and output must contain a standalone `COMPAT_OK` line.

The exit code is 0 when every required suite is PASS/CACHED, 1 for a real
compatibility FAIL in any selected suite, and 2 when required verification is
incomplete. Optional missing prerequisites do not block required-suite success.
Every unselected live suite is explicitly SKIPPED. `--summary` selects the
machine-readable summary; `COMPAT_SUMMARY`, `COMPAT_SUITES`, `COMPAT_REQUIRED`,
and `COMPAT_LIVE=1` provide equivalent environment configuration. Build and run
the binary when preserving exit 2 matters: `go run` remaps nonzero process exits.

## Cache evidence

Cache format 2 is an atomic JSON file with PASS evidence only. Format 1 is a safe
miss. Its identity includes suite, engine, exact version, package/binary digest,
runtime commit, actual checker executable digest, dirty source/contract digest,
pinned schema/fixture digest, non-secret configuration digest, OS and architecture.
Evidence expires after at most seven days; at most 64 identities are retained.
A cache hit is CACHED and reports the original validation time, never a fresh PASS.

Wrapper versions are resolved once, their npm registry SHA integrity is recorded,
and execution pins `package@exact-version`. The artifact identity covers the
wrapper tarball; this is not a lockfile for its entire transitive dependency graph.
Native artifact identity hashes the actual binary. Native caching is disabled
unless a prepared environment supplies `COMPAT_NATIVE_CONFIG_DIGEST`, a digest of
its non-secret effective CLI configuration; `--refresh` always bypasses cache.
Never put credentials or arbitrary environment values in that configuration digest.

Only model/endpoint configuration is hashed from the allowlist; credentials,
URL userinfo/query, and arbitrary environment are excluded. Diagnostic tails are
bounded and redact sensitive environment values and common credential forms.

The scheduled workflow requires wrappers only. Native is a separately gated
manual job on a prepared runner. Actions cache uses unique save keys, an exact
runtime/platform restore prefix, and serialized workflow runs. A save occurs
only after new PASS evidence was persisted. Skipping the save emits a reason.
Issue changes are disabled unless repository variable `COMPAT_MANAGE_ISSUES=true`
is set. When enabled, regression issues close automatically only after a fresh PASS for the identical
recorded matrix; CACHED, incomplete, changed and unscoped historical evidence
requires maintainer review. These workflows have not been executed on GitHub as
part of local verification.

## 中文说明

默认仅运行无凭证合约测试；wrapper/native 实测须显式选择 suite 并传 `--live`，可能产生模型费用。
必需 suite 完整通过或匹配缓存时退出 0；真实兼容失败退出 1；必需前置条件缺失退出 2。
可选 native 缺失不会把已通过的 wrapper 范围判为失败，且仍明确报告 SKIPPED。

缓存绑定精确 engine 版本与工件摘要、运行时提交与实际二进制、源代码、schema/fixture、
非秘密配置、平台；旧格式按 miss 处理，只保存有期限的 PASS 证据。CACHED 不代表本次重新验证。
Actions 使用唯一保存 key、受限恢复前缀及串行执行，不再覆盖不可变 cache key。
只有相同矩阵的新 PASS 能自动关闭对应问题；不同 suite、历史无范围问题和缓存命中不会自动关闭。
