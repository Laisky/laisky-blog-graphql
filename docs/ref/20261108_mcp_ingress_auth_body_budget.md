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

## Second maintainer review (2026-11-15, revision 2a1ab2a)

Research checked on 2026-11-15 against the primary sources linked below.

### Anonymous discovery versus execution (finding A)

- **Status codes.** The MCP authorization spec (2025-11-25 and 2026-07-28 are
  identical here) maps "Authorization required or token invalid" to `401` and
  "Invalid scopes or insufficient permissions" to `403`. Authorization "MUST
  be included in every HTTP request … even if they are part of the same
  logical session". The security best practices add "MCP Servers MUST NOT use
  sessions for authentication."
  <https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization>,
  <https://modelcontextprotocol.io/specification/2025-11-25/basic/security_best_practices>
- **Per-method gating is allowed.** The spec neither requires nor forbids
  open discovery with protected tool calls.
- **Claude's lazy-authentication guide uses that exact design.**
  `initialize` and `tools/list` pass. A protected `tools/call` gets `401`
  "before the MCP SDK runs", including for stateful Streamable HTTP sessions.
  A `200` with `isError: true` triggers no auth prompt; only a transport
  `401` does. <https://claude.com/docs/connectors/building/lazy-authentication>
- **Challenge without credentials.** RFC 6750 §3.1: when "the request lacks
  any authentication information … the resource server SHOULD NOT include an
  error code or other error information". The challenge is therefore
  `Bearer resource_metadata="…"`. A presented but invalid key keeps
  `error="invalid_token"`. <https://www.rfc-editor.org/rfc/rfc6750#section-3.1>
- **`scope=` is left out on purpose.** The MCP spec says servers SHOULD include
  `scope`. These API keys carry no scopes and the server enforces none, so
  naming one would claim an enforcement that does not exist. The agent probes
  and the `invalid_token` challenge omit it too.
- **Batches.** 2025-03-26 clients may send JSON-RPC batches; 2025-06-18
  removed them. mcp-go v1.1.1 decodes one object per POST and answers an
  array with a parse error. The anonymous policy refuses arrays outright
  rather than relying on that.
- **Cancellation (independent review of this follow-up).** The 2026-07-28
  cancellation page: "Streamable HTTP: Closing the SSE response stream is the
  cancellation signal. The server MUST treat a client disconnect as
  cancellation of that request. No `notifications/cancelled` message is
  required or expected." The transport page says the revision "defines no
  client-to-server notifications over Streamable HTTP". The 2025-11-25 page:
  "Invalid cancellation notifications SHOULD be ignored", and receivers "MAY
  ignore" unknown request ids.
  <https://modelcontextprotocol.io/specification/2026-07-28/basic/patterns/cancellation>,
  <https://modelcontextprotocol.io/specification/2026-07-28/basic/transports/streamable-http>
  - **The SDK gap.** mcp-go v1.1.1 still acts on a POSTed
    `notifications/cancelled`. Its `inflightKey` is `sessionID + ":" +
    requestId`, and 2026-07-28 requests have an empty session id. So any
    caller, including an anonymous one, could abort any other caller's modern
    request with the same small integer id.
  - **The fix.** The transport answers a 2026-07-28 `notifications/cancelled`
    with `202` and does not forward it. Anonymous callers may send only
    `notifications/initialized`. Legacy sessions keep cancellation; their
    keys include the HMAC session id.
  - **Classification.** Notifications skip the SDK's `Mcp-Method` header
    validation (`HandleMessage` dispatches id-less messages before it), so
    the guard decodes the body. `"id": null` counts as a notification there.
- **Parser parity.** The policy decodes the routing members with
  `encoding/json` into the same shape as the SDK. Duplicate keys
  (last wins) and case-folded keys (`METHOD`) therefore name the same method
  for both. Anything that does not decode is refused.
- **Open question for the owner.** OAuth-capable clients (Claude, Claude
  Code) start a sign-in flow on a `401` that carries `resource_metadata`.
  `web/public/.well-known/oauth-authorization-server` offers only
  `client_credentials`, and its authorization endpoint is the purchase page,
  so an authorization-code + PKCE flow cannot complete. That was already true
  for the `invalid_token` challenge and the agent probes. Users configure an
  API key header instead.

### Redirects on credential-bearing clients (finding B)

- **Go 1.27 `net/http` redirect policy.** `shouldCopyHeaderOnRedirect` drops
  `Authorization`, `Www-Authenticate`, `Cookie`, `Cookie2`,
  `Proxy-Authorization` and `Proxy-Authenticate` only when the target host is
  neither the first request's host nor a subdomain of it. It compares
  `Hostname()` only.
  - The header is therefore kept on a subdomain, on a port change and on an
    `https`→`http` downgrade of the same host.
  - The 1.27 doc: "sensitive headers are retained on redirect to a subdomain
    or to a different scheme on the same host".
  - Related fixes: CVE-2023-45289, CVE-2024-45336 (stripping now persists for
    the rest of the chain) and CVE-2025-4673.
  <https://github.com/golang/go/blob/release-branch.go1.27/src/net/http/client.go>,
  <https://pkg.go.dev/vuln/GO-2025-3420>
- **Practice.** `CheckRedirect` returning `http.ErrUseLastResponse` returns
  the 3xx itself, with its body unclosed. A shallow copy `c := *base` is
  idiomatic: `http.Client` has only Transport, CheckRedirect, Jar and Timeout,
  and the copy shares the connection pool.
  `library/httpclient.WithoutRedirects` does this for the validator (any
  injected client) and for the one-api consume call.
- **Production check.** `GET /api/token/balance` and
  `POST /api/token/consume` on `https://oneapi.laisky.com` answer `401` with
  no redirect (checked 2026-11-15), so refusing redirects changes nothing for
  the primary deployment.

### Revalidation capacity (finding C)

- **one-api routes.** `router/api.go` registers both
  `POST /api/token/consume` and `GET /api/token/balance` with
  `middleware.TokenAuth()`. A consume that answered `200` therefore passed the
  same credential check as a validation, at the moment it ran. Every MCP tool
  invocation already sends one, either the paid charge or the zero-cost usage
  report. Refreshing the key's trust from it removes the revalidation demand
  of active keys, instead of coordinating more of it.
- **Kept conservative.**
  - Only the validator's own proof counts: `200`, `application/json`,
    `"success": true` (`oneapi.ConsumeReceipt`). An independent review showed
    that a plain `err == nil` let a `200` HTML page keep a revoked key alive.
    The consume's billing classification is unchanged.
  - The refresh is dated when the consume started.
  - It applies only to a key the validator already accepted, and only when
    the consume went to the validator's own base URL (path prefix included).
  - It runs under the verdict lock, so it cannot undo a rejection recorded
    meanwhile.
  - Refusals are ignored: one-api's consume handler can refuse for
    quota-per-call reasons that `TokenAuth` alone would not.
- **Coordinating budgets across replicas**, for when that is ever needed:
  - Shared GCRA in Redis (go-redis/redis_rate v10.0.1, atomic Lua using the
    Redis `TIME` clock) keyed by egress address.
  - A static per-replica split (budget/N), which wastes capacity and drifts
    under autoscaling.
  - A dedicated egress component that owns the limit.

  A Redis limiter makes Redis a hard dependency of authentication. Failing
  closed turns a Redis outage into a full outage; failing open risks the
  egress address being banned. A short Redis timeout with a budget/N local
  fallback is the usual hybrid. None is built in: the primary deployment is
  one process, and the manual documents the division.
  <https://pkg.go.dev/github.com/go-redis/redis_rate/v10>

### Unarmed read deadlines (finding D, P3)

- **Gap.** `httpbody.Enforce` keeps serving a request whose read deadline
  `http.ResponseController` cannot arm (a writer chain without `Unwrap`), with
  only the byte cap. Both callers logged that at DEBUG, so the slow-body
  guarantee could silently disappear on an entry.
- **Change.** `httpbody.UnarmedReporter` counts every such request in
  `http_request_body_read_deadline_unarmed_total{entry}` (`mcp` for the
  transport, the route class for gin) and admits one WARN per entry and
  minute with a suppressed count. Failing the request instead was not chosen:
  an unarmable writer is a deployment property, and refusing every body on it
  would turn a lost protection into an outage.
- **HTTP/2.** Go's HTTP/2 server implements `SetReadDeadline` per stream, so
  the same contract holds over h2; `TestReadDeadlineCutsSlowBodyOverHTTP2`
  runs it over TLS next to the HTTP/1.1 real-socket tests.

### Validation budget bounds (finding E, P3)

- **Gap.** `LoadValidationSettings` clamped TTLs but accepted any cache size,
  source count, concurrency, rate or burst.
- **Change.** `CheckValidationBounds` runs in startup validation and again in
  `configureMCPAuthentication`. It refuses sanity ceilings (1,000,000 cache
  entries or sources, 256 concurrent calls, 1000/s, burst 100,000), non-finite
  rates, a per-source budget above the global one, and a gate whose
  `ValidationCapacity.MaxCallsPerWindow` exceeds the new
  `oneapi_rate_limit` (default 480, one-api's `GLOBAL_API_RATE_LIMIT`; `0`
  for an exempt egress). The defaults (266 calls per window) and the example
  settings pass; `TestExampleSettingsPassValidationBounds` reads the example
  through the production config loader.
- **Why refuse, not clamp.** Clamping silently runs a different configuration
  from the one written; every other refused value in this namespace (disabled
  validation in prod mode, a bad public origin) also fails startup.

## Third maintainer review (2026-11-16, revision cce5895)

The review asked for the authenticator to be complete rather than patched per finding. Two read-only sweeps (every credential reader, every one-api caller) found the gaps listed below; `TestIngressInventory` now keeps both inventories exact (`docs/arch/mcp_auth.md`).

### one-api rate limits (source: `one-api` repo, `middleware/rate_limit.go`, `router/*.go`)

- The `/api` group (`GlobalAPIRateLimit`, mark `GA`) is keyed by `c.ClientIP()`. Redis and memory limiters keep the last `GLOBAL_API_RATE_LIMIT` accepted timestamps and refuse while the oldest is younger than the window (`int64(now.Sub(old).Seconds()) < 180`): a sliding window that counts accepted calls only, with whole-second truncation (a call 180.9 s old still counts). `oneapi.CallLedger` therefore keeps window + 1 one-second slots and counts calls when they start, which can only overcount.
- `/api/token/consume`, `/api/token/balance` and `/api/user/get-by-token` are all in that group, so validations, consumes and the former Telegram check shared one budget.
- Relay routes (`/v1/...`) use `GlobalRelayRateLimit` (mark `GR`), keyed by a hash of the token, not the client address. Embedding, rerank and Responses calls made with callers' keys therefore do not draw on the shared per-IP budget.

### Findings and changes

- **Shared capacity (P2).** Consumes (MCP and GraphQL tools, ArweaveUpload, Telegram uploads) are recorded in a process-wide ledger; the gate admits against it and refuses once `oneapi_rate_limit` calls are in the window, so validation yields to billing instead of pushing the egress into `429`. A full window is a local refusal: no stale grace, because the conservative rule (no local refusal extends a key) is simpler to reason about than proving the window cannot be filled by unauthenticated traffic.
- **Ungated validation callers.** Telegram key linking called `GET /api/user/get-by-token` directly (own accept rule, hard-coded origin, redirects followed, no cache or budget), once per chat message. ArweaveUpload forwarded any `Authorization` header straight to a consume. Both now authenticate through the shared authenticator; Telegram chats are named budget sources.
- **Gateways (P2).** One address onboards 5 keys, then one per 10 s. `gateways` gives listed ranges (matched on the trusted-proxy-resolved address, never on raw headers) their own budget within the global one. Alternatives considered: trusting a gateway's forwarding header (rejected: any peer could claim a range), raising the default per-source rate (rejected: weakens single-source flood isolation for everyone).
- **Legacy endpoint (P2).** `settings.oneapi.balance_url` was an exact URL on master (`general.go` used it verbatim). It is honored verbatim again (`NewOneAPITokenValidatorAt`); only a value ending in `/api/token/balance` names a one-api base for consume observation.
- **Canceled waiter (P3).** A waiter whose own context ended returned `ErrValidationUnavailable` without an authority classification but was still served from stale grace. Stale grace now requires `errAuthorityNoVerdict`.
- **Length bound (P3).** The 256-byte bound moved into `DeriveFromAPIKey` (and a 2 KiB bound on the whole header into the parser), so no path hashes an oversized key; the GraphQL fingerprint and Telegram paths are bounded too.
- **Identity types (section 4).** `askuser.AuthorizationContext` is an alias of `mcpauth.Context`, so the verified bit travels with it; `FromSharedContext` refuses unverified contexts; `files.AuthFromVerified` is the only conversion to the files view, and the file tools' unverified context fallback (reachable only from tests) is gone.
- **Redirects.** The embedder, rerank, Responses and PageIndex clients carried keys and followed redirects; they now refuse them like the validator and consume.
- **Body memory (P3).** An in-flight body budget for the MCP transport counts bytes actually read (not declared lengths, which would let slow senders reserve the budget without sending), covering the SDK's own copy.
