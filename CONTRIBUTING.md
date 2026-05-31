# Contributing

Thanks for contributing to `acp-runtime-go`.

## Development

```bash
make build
make lint
make test
make harness-admission
```

## Scope

Please keep changes aligned with the repository's current goals:

- ACP runtime architecture and RFCs
- `acp-simulator-agent`
- harness-driven protocol and scenario validation

## Pull Requests

Before opening a PR, make sure:

- tests pass locally
- docs stay in sync with behavior changes
- English entry-point docs remain the default, with Chinese translations updated when needed
- Go module changes keep the public module path stable
