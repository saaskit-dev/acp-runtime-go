# Project Scenario Matrix

Language:
- English (default)
- [简体中文](zh-CN/research/project-scenario-matrix.md)

## Summary

This matrix captures multi-step product scenarios that matter for real integration quality.

It complements the protocol coverage matrix:

- protocol coverage checks whether a capability exists and works
- scenario coverage checks whether combined workflows are stable enough for product use

Current note:

- simulator-specific baseline scenarios now live in dedicated harness cases such as `21-simulator-available-command-surface`, `23-simulator-scenario-full-cycle`, and `24-simulator-fault-injection`
- generic scenario names like `scenario.read-file` and `scenario.run-command` remain useful for ecosystem agents, but they should not be treated as the simulator's only baseline
- permission denial is now tracked in two layers: `scenario.permission-denied` for the request path, plus outcome cases such as `scenario.permission-denied-cancelled`, `scenario.permission-denied-end-turn`, and `scenario.permission-mode-denied`
## Translation

- [简体中文](zh-CN/research/project-scenario-matrix.md)
