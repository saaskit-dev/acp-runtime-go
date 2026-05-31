# Runtime SDK API Coverage

Language:
- English (default)
- [简体中文](../zh-CN/guides/runtime-sdk-api-coverage.md)

Current Go coverage:

| Surface | Evidence |
| --- | --- |
| `Runtime.StartSession` | `runtime_test.go` |
| stdio ACP transport | `runtime_test.go` |
| simulator prompt flow | `runtime_test.go` |
| simulator write/tool operation projection | `runtime_test.go` |
| command builds | `runtime_test.go` |
| harness admission path | `make harness-admission` |

Before merging larger changes, run:

```bash
go vet ./...
go test ./...
make build
make harness-admission
```
