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
- The client source is `clientip.FromContext`, the identity PR #92's
  request-identity middleware resolves with the trusted-proxy policy. A direct
  peer cannot choose its source with `X-Forwarded-For`. IPv6 is grouped by /64
  via `clientip.NetworkKey`.
- Review follow-ups (2026-11-10):
  - **One gate for every one-api call.** All calls to one-api's per-IP-limited
    REST API go through a single `UpstreamGate`: validation, revalidation, the
    `/api/oneapi/balance` proxy and GraphQL `ValidateOneapiAPIKey`. Before
    this, the anonymous balance proxies called one-api unthrottled. About
    2.7 req/s kept one-api answering 429, which made every uncached valid key
    503.
  - **Revalidation reserve.** Known keys revalidate from a reserved budget
    that unknown-key traffic cannot spend. Stale-if-error alone could not
    help here: a sustained unknown-key flood still locked active keys out
    after positive TTL + grace (35 minutes).
  - **Genuine failures only.** Stale-if-error applies only to failures
    reported by one-api itself. A refusal by this server's own budget never
    applies it; otherwise emptying one's own budget extended a revoked key
    for about 35 minutes.
- one-api reports some controller failures as HTTP 200 `{"success": false}`
  (`helper.RespondError`), and its SPA serves 200 HTML outside `/api`. The
  validator accepts only `application/json` with `"success": true` from a
  bounded read.
- Minimum request-body data rate: modeled on ASP.NET Core Kestrel's
  `MinRequestBodyDataRate` (default 240 B/s after a 5 s grace; see
  <https://learn.microsoft.com/aspnet/core/fundamentals/servers/kestrel/options>).
  We use 4 KiB/s after 10 s, with an absolute cap of
  min(30 min, grace + cap / rate). Since 2026-11-15 the earned read time is
  also bounded by `max_idle` (see the follow-ups below), because a cumulative
  rate lets a burst fund a later stall.
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

## Maintainer review follow-ups (2026-11-15)

Sources verified on 2026-11-15 for the review of PR #95 at `2cbf779`.

### Request-body progress (finding A)

How common servers bound a slow request body:

- **nginx `client_body_timeout`** (default `60s`, answers 408) is an
  inactivity timer. It runs "only for a period between two successive read
  operations, not for the transmission of the whole request body".
  <https://nginx.org/en/docs/http/ngx_http_core_module.html#client_body_timeout>
- **ASP.NET Core Kestrel `MinRequestBodyDataRate`** (default 240 B/s after a
  5 s grace) is cumulative. The heartbeat computes bytes read divided by the
  time spent reading since the body started, and there is no separate body
  idle timer.
  <https://learn.microsoft.com/en-us/dotnet/api/microsoft.aspnetcore.server.kestrel.core.kestrelserverlimits.minrequestbodydatarate>
  and
  <https://github.com/dotnet/aspnetcore/blob/main/src/Servers/Kestrel/Core/src/Internal/Infrastructure/TimeoutControl.cs>
- **Apache `mod_reqtimeout`** (`body=20,MinRate=500` by default) extends a
  stage deadline by bytes / MinRate for each chunk. It is cumulative too,
  optionally capped by `timeout-maxtimeout`.
  <https://httpd.apache.org/docs/2.4/mod/mod_reqtimeout.html>
- **Envoy `stream_idle_timeout`** (default 5 min) resets on every data event.
  **HAProxy `timeout client`** is an inactivity timeout with no default.
  <https://www.envoyproxy.io/docs/envoy/latest/api-v3/extensions/filters/network/http_connection_manager/v3/http_connection_manager.proto>
  and <https://www.haproxy.org/download/3.2/doc/configuration.txt>

Conclusion for `library/httpbody`:

- **Cumulative credit.** A cumulative rate (Kestrel, Apache) lets a burst fund
  a later stall. The PR's first version had this property: 1 MiB at once
  bought about 4.5 minutes at 4 KiB/s.
- **Inactivity timer.** An nginx-style timer alone stops the pure stall, but
  it still admits a burst followed by a trickle just under the idle bound.
- **Token bucket.** `library/httpbody` now holds read time as a token bucket:
  every byte adds `1/min_data_rate` s, capped at `max_idle` ahead of the
  delivery, and the absolute cap still applies. This bounds both the stall
  and the trickle. A live body delivers at least
  `min_data_rate × (t − max_idle)` over any interval `t`.
- **Default.** `max_idle` defaults to 30 s, between Kestrel's 5 s grace and
  nginx's 60 s inactivity timer, and is never below `read_grace`.

### Upstream slots before tokens (finding B)

- **Peeking.** golang.org/x/time/rate v0.16.0 `Limiter.TokensAt(t)` reads the
  token count without consuming it: `advance` "does not mutate lim".
- **Refunds.** `Reservation.CancelAt` restores
  `tokens − tokensFromDuration(lastEvent − timeToAct)`. Later reservations
  reduce the refund. It is a no-op once `timeToAct` is before `t`. A token
  taken before a slot wait therefore cannot be refunded reliably after the
  wait.
  <https://github.com/golang/time/blob/master/rate/rate.go>
- **Resulting order.** The gate now peeks first (`TokensAt`) for a fast
  refusal, then waits for a slot, then charges.

### Protected-resource metadata URL (finding C)

- **Constructing the URL.** RFC 9728 §3.1 inserts
  `/.well-known/oauth-protected-resource` between the host and any path of the
  resource identifier.
- **Matching resource.** §3.3: the document's `resource` MUST equal the
  identifier the client used, otherwise the client MUST NOT use it.
- **Advertising it.** §5.1 defines `resource_metadata` in
  `WWW-Authenticate`. <https://www.rfc-editor.org/rfc/rfc9728.html>
- **Host headers.** The RFC has no explicit Host-header rule. §7.3
  (impersonation) and §7.7 (SSRF) motivate deriving the URL from operator
  configuration only, never from a request's `Host` or `X-Forwarded-Host`.
- **MCP 2025-11-25 authorization.** Servers MUST implement RFC 9728 and offer
  the `WWW-Authenticate` header or the well-known URI. Clients use the header
  when present, otherwise they probe the path-inserted URI, then the root.
  <https://modelcontextprotocol.io/specification/2025-11-25/basic/authorization>
