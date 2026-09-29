# PR #51 review: request identity and legacy session ownership

## Confirmed defect and reproduction

The previous tools/list middleware cached an API-key identity under a caller-supplied
session ID before transport validation. Missing or malformed Authorization then
reused that identity. This leaked caller-specific tool preferences even on modern
stateless requests containing an extra legacy session header. The SDK's default
legacy manager only checked the UUID shape, not the credential owning the session.

The tests-only commit `8b2c748a34bfac9a3abceb47777c2fd7e5106a19` reproduced the
problem on the production HTTP handler in [run 36607704142](https://github.com/Laisky/laisky-blog-graphql/actions/runs/36607704142).
The existing protocol suite/build/vet passed; the new ownership, modern-catalog
and concurrent-caller tests failed with actual 200/202 responses instead of 4xx.
A synthetic leaf tool also executed through the reused legacy session. This proves
the missing transport boundary, not a bypass of every production tool's separate
billing/authorization checks. No real file operation or paid model was configured.
The first reproduction unintentionally emitted rejected zero-cost billing audit
events with synthetic keys; the fixture now explicitly disables that reporter.

## Implementation contract

- Resolve identity exclusively from the current request's canonical credentials.
  Remove the session-to-identity cache entirely, including its pre-validation writes
  and missing/invalid-credential fallback. There is no cache TTL or eviction policy
  to get wrong, and no unbounded identity map.
- Supply the SDK's request-scoped `SessionIdManagerResolver`. Credentialed legacy
  sessions contain a random SDK nonce and a domain-separated HMAC-SHA256 tied to
  the parsed API key. Verification uses `hmac.Equal`. Neither the key nor its hash
  is serialized. This is namespace binding, not validation of an API key's current
  billing status, and assumes secret, high-entropy API keys.
- Recompute the binding from each request, with no per-instance ownership map and
  no new deployment secret. Same-credential requests work on another replica.
  This does not make an active SSE stream or other in-memory SDK state distributed.
- Check both Validate and Terminate; the public manager must not permit an anonymous
  DELETE to clean up a credential-bound session. Requests with missing, malformed,
  changed, or tampered credentials/session proofs are rejected before dispatch.
- Preserve anonymous public discovery, canonical query-key compatibility, and
  explicit Authorization-header precedence. Anonymous sessions cannot later be
  rebound to an authenticated identity.
- Modern MCP 2026-07-28 requests remain stateless and ignore legacy session identity.
  An extra session header does not select another user's preferences. Public
  discovery on absent/malformed credentials remains public, not authenticated.
- Tool authorization, billing, namespace isolation and user preference semantics
  remain separate. Disabling a tool in the catalog is not an authorization rule.

## Client migration and limits

Authenticated legacy clients must initialize a new session after rollout and send
**the same API key on every request**, including notifications, GET and DELETE.
Old unsigned IDs are intentionally not accepted on credentialed requests. Switching
API keys, or switching an anonymous connection to an authenticated one, requires
reinitialization. Prefer Authorization headers; query keys remain a compatibility
path and should not be placed in logs or shared links. Do not blindly replay a
state-changing tool call when reestablishing a session.

As with the previous SDK stateless-generating manager, DELETE cleans up transport
state but does not persist a revocation list or guarantee that a proof can never
be reused. Credential revocation/billing enforcement remains request/tool scoped.
No global `WithStateLess(true)` toggle, OAuth claim, production migration or deploy
is part of this fix.

## Regression matrix

| Boundary | Coverage |
| --- | --- |
| Three legacy revisions | 2025-03-26, 2025-06-18, 2025-11-25; real issued session IDs required |
| Cross-owner / absent / malformed credentials | tools/list, tools/call, initialized notification, GET and DELETE reject |
| Side effects | Atomic leaf counter stays unchanged for rejected tool requests |
| Owner remains usable | Catalog and tool-call positive controls after rejected attempts |
| Modern extra legacy header | Absent, malformed and alternating A/B credentials never inherit old identity |
| Concurrency | 24 parallel owner/other/swapped-session requests and final owner check |
| Canonical credential forms | Three query aliases, raw/Bearer/legacy-prefix forms, header precedence |
| Replica independence | Session initialized on one handler, used on a second with same credential |
| Tampering | Missing/truncated/invalid-hex/forged/recombined authenticator and bad nonce |
| Public discovery | Anonymous handshake remains usable but cannot be rebound to a credential |

Run `go test -race -count=20 -timeout 5m ./internal/mcp -run TestMCPReview` for
repeated targeted acceptance and `go test -race -cover -timeout 5m ./...` for the
full suite. The PR compatibility workflow runs the full suite on every update.
Final run links and audit outcomes are recorded in the PR discussion.

## Primary references

- [MCP security best practices: session hijacking](https://modelcontextprotocol.io/specification/2025-11-25/basic/security_best_practices)
- [mcp-go v1.1.1 session resolver and default managers](https://github.com/mark3labs/mcp-go/blob/v1.1.1/server/streamable_http.go)
- [MCP 2026-07-28 authorization](https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization)
