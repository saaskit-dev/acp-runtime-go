// Package openai provides a local, single-user text compatibility gateway for
// Chat Completions and Responses. A shared API key and session owner hashes do
// not provide multi-tenant identity or filesystem isolation. Agent discovery is
// not an execution allowlist. Keep the gateway on loopback unless a separately
// reviewed deployment enforces authentication, authorization, sandboxing and
// resource limits appropriate to its users.
//
// SessionTTL is idle time: active turns retain a lease, and idle expiry restarts
// at turn completion. Expired or removed records cannot acquire a new lease.
// Caller disconnection quarantines a persistent session; clients must create a
// fresh handle rather than reusing a transport with unconfirmed remote work.
//
// Streaming emits assistant text only. Runtime preview events may be dropped:
// a missing suffix is recovered from the authoritative completion, while a
// non-prefix gap emits stream_gap without a success terminal. Tool/plan previews
// are not injected into the assistant answer. Output byte and write deadlines
// bound gateway delivery, not provider execution, provider memory, or billing.
//
// Images, token limits, stop sequences and non-default sampling parameters are
// rejected. Temperature=1 and top_p=1 are accepted as compatibility no-ops; the
// provider controls its actual defaults. Omit those fields when exact sampling
// semantics matter. Stored response aliases are bounded process-local indexes,
// not durable Responses API storage.
package openai
