# Agent Admission Checklist

Language:
- English (default)
- [简体中文](zh-CN/research/agent-admission-checklist.md)

## Summary

This checklist defines the minimum evidence and pass criteria required before a new agent can be admitted into `acp-runtime`.

It requires:

- protocol coverage review
- project-scenario regression review
- permission and mode research
- durable artifacts such as transcripts, summaries, and notes

Artifacts should now be stored under:

```text
.tmp/harness-outputs/<agent>/<timestamp>/
```

When reviewing scenario evidence, `matrix-summary.json` should now be treated as the first-pass gate summary.
At minimum it should expose:

- whether all applicable `P0` scenarios passed
- which required `P0` scenarios failed
- which permission behavior families were observed
- which expected permission behavior families are still missing from evidence

`make harness-admission` is the deterministic simulator admission gate. For real
agents, build `bin/acp-harness` and pass `--type <agent>` with a case set that
matches the target admission scope. The command should fail with a non-zero exit
code only when admission blockers remain.
At minimum, blockers include:

- any applicable `P0` scenario failure
- missing evidence for any permission family that has an applicable scenario case for that agent

`make harness-full` remains the stricter local matrix command and may still skip
cases that have not yet been implemented in the Go harness.
## Translation

- [简体中文](zh-CN/research/agent-admission-checklist.md)
