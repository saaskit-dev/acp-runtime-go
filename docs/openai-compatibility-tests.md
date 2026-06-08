# OpenAI Compatibility Test Sources

This gateway targets a practical OpenAI-compatible subset for ACP-backed text chat.
Compatibility tests should make unsupported OpenAI features explicit instead of
silently accepting requests that cannot be honored.

## Sources

- `openai/openai-node`
  - Path: `tests/api-resources/chat/completions/completions.test.ts`
  - Use: generated SDK request-shape examples for Chat Completions.
  - Local coverage: `TestChatCompletionsAcceptsOpenAINodeGeneratedCreateParams`.

- `openai/gpt-oss`
  - Path: `compatibility-test/`
  - Use: external API compatibility checks for tool/function calling and API
    response shape.
  - Current status: not suitable as a passing CI check until this gateway
    implements OpenAI `tool_calls`. Use it as a failing external acceptance
    suite for the tool-calling milestone.

- `openai/openai-openapi`
  - Path: `openapi.yaml`
  - Use: canonical field and endpoint inventory for deciding whether a field is
    supported, rejected, or intentionally ignored.

## Local Policy

- Accept text-only Chat Completions requests that common OpenAI SDKs emit.
- Reject unsupported semantic features with an OpenAI-style error response.
- Do not claim support for tool calling, structured JSON schema output, audio
  modalities, or multiple choices until those behaviors are implemented.
- Preserve OpenAI stream invariants that clients depend on, such as stable chunk
  IDs within one stream.

## External Commands

The reference repositories are cloned under `~/`:

```sh
git clone --filter=blob:none --sparse https://github.com/openai/gpt-oss.git ~/gpt-oss
git -C ~/gpt-oss sparse-checkout set compatibility-test

git clone --filter=blob:none --sparse https://github.com/openai/openai-node.git ~/openai-node
git -C ~/openai-node sparse-checkout set --no-cone tests/api-resources/chat/completions src/resources/chat src/resources/shared.ts

git clone --filter=blob:none https://github.com/openai/openai-openapi.git ~/openai-openapi
```

When OpenAI tool calling is implemented, configure
`~/gpt-oss/compatibility-test/providers.ts` with this gateway and run:

```sh
cd ~/gpt-oss/compatibility-test
npm install
npm start -- --provider acp-openai -n 1 -k 1
```

Current external result against `127.0.0.1:18080` with provider `acp`:

```text
Case 0 failed: 400 OpenAI tool calling is not supported by this gateway yet
pass@k (k=1): 0.000
```

This is the expected result until `tool_calls` are implemented.
