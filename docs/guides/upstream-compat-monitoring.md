# Upstream Wrapper Compatibility Monitoring

This runtime launches ACP agents via their npm wrapper packages (currently
unpinned, so npm resolves `latest` on each spawn). That keeps the wrappers in
sync with upstream, but it also means a breaking change in a wrapper release
can silently break the runtime. This guide documents the monitoring mechanism
that catches such regressions automatically.

## How it works

A scheduled GitHub Actions workflow (`.github/workflows/compat-check.yml`) runs
daily at 06:00 UTC. It invokes `cmd/acp-compat-check`, which for each wrapper:

1. Queries `npm view <pkg> version` for the current latest version.
2. If the corresponding API key is present, spawns the real agent and runs a
   minimal prompt (`Reply with exactly: COMPAT_OK`), asserting the full chain
   (spawn → initialize → session/new → session/prompt → output) works.
3. Reports PASS / FAIL / SKIPPED per agent.

When a test **fails** (exit code 1), the workflow opens (or updates) a GitHub
Issue titled `[compat-check] ACP wrapper compatibility regression detected`,
labeled `compat-regression`, with the full check output and a link to the CI
run. When the check later **passes** again, the issue is automatically closed.

## Configuring secrets

The real-agent smoke tests need API keys. Configure them as repository secrets
under **Settings → Secrets and actions → Actions**:

| Secret               | Used by          | Required? |
| -------------------- | ---------------- | --------- |
| `ANTHROPIC_API_KEY`  | claude-agent-acp | optional  |
| `OPENAI_API_KEY`     | codex-acp        | optional  |

Agents whose key is absent are **skipped**, not failed — so partial
configuration is fine. `CODEX_API_KEY` is also accepted as an alias for the
codex smoke test.

The keys reach the spawned wrapper via the runtime's `envSlice` (which merges
`os.Environ()`), so no Go code change is needed — the CI step simply exports
the secrets as environment variables.

## Running locally

The same check runs locally with no CI setup:

```bash
# Without keys: reports SKIPPED (exit 2)
go run ./cmd/acp-compat-check

# With a key: runs the real spawn + prompt smoke test (exit 0 = pass, 1 = fail)
ANTHROPIC_API_KEY=sk-ant-... go run ./cmd/acp-compat-check
```

Sample output:

```text
acp-compat-check — 2026-07-05 16:48:39 UTC

claude-agent-acp: latest=0.55.0
  spawn+prompt: PASS (output="COMPAT_OK", 3.2s)

codex-acp: latest=1.1.0
  spawn+prompt: SKIPPED (no OPENAI_API_KEY)

Result: PASS — all tested agents are compatible.
```

## Exit codes

| Code | Meaning |
| ---- | ------- |
| 0    | All tested agents passed. |
| 1    | At least one agent failed (regression detected). |
| 2    | All agents were skipped (no API keys configured). |

## Manual dispatch

The workflow can be triggered on demand from the GitHub Actions UI
(**Actions → compat-check → Run workflow**). This is useful after bumping a
wrapper or investigating a reported regression.

## What a regression looks like

A typical regression: the wrapper accepts the prompt and the underlying model
runs (cost is incurred), but no `agent_message_chunk` is emitted and the output
is empty. The smoke test detects this because `outputText` lacks the sentinel
token `COMPAT_OK`, and the issue body will show the empty output alongside the
wrapper version number so the regression can be triaged quickly.
