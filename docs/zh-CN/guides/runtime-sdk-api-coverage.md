# Runtime SDK API 覆盖

[English](../../guides/runtime-sdk-api-coverage.md)

当前 Go 覆盖：

| Surface | Evidence |
| --- | --- |
| `Runtime.StartSession` | `runtime_test.go` |
| stdio ACP transport | `runtime_test.go` |
| simulator prompt flow | `runtime_test.go` |
| simulator write/tool operation projection | `runtime_test.go` |
| command builds | `runtime_test.go` |
| harness admission path | `make harness-admission` |

较大改动交付前运行：

```bash
go vet ./...
go test ./...
make build
make harness-admission
```
