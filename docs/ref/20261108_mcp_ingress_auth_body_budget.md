# MCP ingress: API key validation and request-body budgets (2026-11-08)

Sources verified for issues #86 (any non-empty bearer became a tenant) and #88
(unbounded MCP request bodies). Cited by `internal/mcp/auth`,
`internal/mcp/server_ingress.go`, `library/httpbody` and `docs/manual/mcp.md`.

## Bearer rejection semantics

- MCP authorization, revision 2026-07-28 (current per
  <https://modelcontextprotocol.io/specification/versioning>),
  <https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization>:
  "Invalid or expired tokens MUST receive a HTTP 401 response". Servers must
  respond per OAuth 2.1 §5.3. They advertise protected-resource metadata
  through `resource_metadata` in `WWW-Authenticate` on 401, or through the
  well-known URI. Revision 2025-06-18 made the header mandatory. The 403
  `insufficient_scope` example pairs `error` with `resource_metadata`.
- RFC 6750 §3/§3.1 (<https://www.rfc-editor.org/rfc/rfc6750.html>): the
  `invalid_token` error is used for expired, revoked or malformed tokens.
  - Status is 401, with `WWW-Authenticate: Bearer error="invalid_token", error_description="..."`.
  - Each auth-param appears at most once; other auth-params MAY be added.
  - A request that lacks any authentication information SHOULD NOT receive an
    error code. This server therefore keeps anonymous discovery without an
    `error` attribute and treats a bearer scheme with an empty token as "no
    credential".
- RFC 9728 §5.1 (<https://www.rfc-editor.org/rfc/rfc9728.html>): the
  `resource_metadata` parameter MAY be combined with other parameters. The
  status code is defined by RFC 6750.
- RFC 9110 (<https://www.rfc-editor.org/rfc/rfc9110.html>):
  - 413 (§15.5.14): the server MAY close the connection, so we send
    `Connection: close`.
  - 503 (§15.6.4): the server MAY send `Retry-After`. We use it when the
    credential authority cannot give a verdict.

## Caching remote credential validation

- RFC 7662 §4 (<https://www.rfc-editor.org/rfc/rfc7662.html>): caching
  validation results trades revocation latency for load. "Highly sensitive
  environments can opt to disable caching entirely."
- OWASP Microservices Security Cheat Sheet: bound cache duration to the
  required freshness.
- Shared refusal budgets on credential paths become lockout levers. This repo
  already rejected them in `cloud_monitoring_webhook_20261103.md` and
  `telegram_alert_admission_20261104.md`, citing the OWASP Authentication Cheat
  Sheet. The global upstream budget is still required, because it protects
  one-api's per-IP limit. A per-source token bucket in front of it therefore
  keeps one source from spending all of it:
  - a source is an IPv4 address or an IPv6 /64;
  - sources live in a bounded LRU;
  - a source is charged only when an upstream call starts;
  - a source over its budget is refused without spending the global budget.
- Residual risk: a distributed flood from many sources can still exhaust the
  global budget. Recently validated keys survive it through the positive cache
  and stale grace.
- Until PR #92's `library/clientip` lands, the source is gin's `ClientIP()`,
  which trusts `X-Forwarded-For` from any peer (gin 1.12 default trusted
  CIDRs). The source is threaded through `mcpauth.WithClientAddress` so that
  it can be swapped for `clientip.FromContext`.
- No normative source covers negative caching or stale-if-error. Our choices:
  - Short negative TTL (≤ 5m).
  - Stale-if-error only for keys accepted within a bounded grace window.
  - Every other key fails closed with 503.
  - Upstream calls for uncached keys are rate- and concurrency-limited, so
    random-bearer floods cannot amplify.

## one-api as the authority (source: `one-api` repo, read-only)

- `router/api.go`: `GET /api/token/balance` sits behind
  `middleware.TokenAuth()` under the `/api` group with `GlobalAPIRateLimit()`.
  It is read-only (`controller.GetTokenBalance`). `POST /api/token/consume` is
  the billing route the paid tools already call.
- `middleware/auth.go` `TokenAuth`:
  - Any `ValidateUserToken` error leads to 401. Unknown, expired, disabled and
    quota-exhausted tokens (`requireQuota=true`) all get 401, and so does a
    token-store failure (`errkind.ServerErr` is still answered with 401).
  - A subnet mismatch against one-api's own `ClientIP` (this server) leads to
    403, as does a disabled or banned owner. Owner lookup failure is 500.
  - Only `ValidateUserTokenForTask` (async video task reads) skips the quota
    check. There is no general quota-agnostic validation route, so exhausted
    tokens cannot be validated without modifying one-api.
- `middleware/rate_limit.go`: the `/api` group is limited per client IP by
  `GLOBAL_API_RATE_LIMIT` (default 480 per 180s). All validations and paid-tool
  consumes from this server share that budget, which is why validation is
  rate-limited locally (default 1/s, burst 30).

## Go 1.27 net/http deadlines (source: `$GOROOT/src/net/http/server.go`, go1.27.3)

- `http.Server.ReadTimeout` sets a whole-request deadline at `readRequest`.
  The background read that detects client disconnects starts immediately for
  bodyless requests, or at body EOF via `registerOnHitEOF`.
- `connReader.startBackgroundRead` calls `cr.rwc.SetReadDeadline(time.Time{})`
  before reading. An already expired request deadline therefore does not
  cancel a long-lived handler through that read.
- A timed-out background read that was not aborted calls
  `handleReadErrorLocked`, which calls `conn.cancelCtx()`. A deadline armed
  after the background read started (for example through
  `http.ResponseController.SetReadDeadline`) would cancel the request context.
- `library/httpbody` arms a per-request deadline only for requests with a
  body, before any read, and clears it at EOF. It never depends on that
  ordering, and it allows different budgets per route.
- `http.NewResponseController` needs every writer wrapper to implement
  `Unwrap() http.ResponseWriter`. Wrappers we checked:
  - gin's `responseWriter` implements it.
  - gin-middlewares v7 does not replace the writer.
  - The MCP `loggingResponseWriter` gained `Unwrap`.
- mcp-go v1.1.1 `StreamableHTTPServer` reads POST bodies with an unbounded
  `io.ReadAll` and answers read errors with a JSON-RPC parse error. The cap
  must therefore be installed before the SDK.
