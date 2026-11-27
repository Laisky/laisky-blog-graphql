# Remote MCP Server Manual

This guide explains how to connect to the remote Model Context Protocol (MCP) endpoint exposed by the GraphQL service, the tools that are available, the HTTP helper pages that accompany them, and the configuration that must be in place before use.

## Menu

- [Remote MCP Server Manual](#remote-mcp-server-manual)
  - [Menu](#menu)
  - [Overview](#overview)
  - [Deployment Prerequisites](#deployment-prerequisites)
  - [Authentication Model](#authentication-model)
    - [Bearer Tokens](#bearer-tokens)
    - [Credential Validation](#credential-validation)
    - [Request Size and Time Limits](#request-size-and-time-limits)
  - [HTTP Endpoints](#http-endpoints)
  - [Tool Reference](#tool-reference)
    - [`web_search`](#web_search)
    - [`ask_user`](#ask_user)
      - [Human Console Workflow](#human-console-workflow)
    - [`get_user_request`](#get_user_request)
      - [User Requests Console Workflow](#user-requests-console-workflow)
    - [`extract_key_info`](#extract_key_info)
    - [`mcp_pipe`](#mcp_pipe)
    - [Image Messages](#image-messages)
    - [Data Storage Notes](#data-storage-notes)
  - [Client Integration Tips](#client-integration-tips)
  - [Troubleshooting](#troubleshooting)

## Overview

The service mounts an MCP-compatible JSON-RPC endpoint at `/mcp`. Clients may connect over HTTP(S) using any MCP client, such as the [MCP Inspector](https://modelcontextprotocol.io/), and interact with these tools:

**`https://mcp.laisky.com` does not require the suffix `/mcp` when used as an MCP endpoint.**

- `web_search` — runs a Google Programmable Search query.
- `web_fetch` — renders a dynamic web page through the Redis-backed fetcher.
- `ask_user` — forwards a question to the authenticated human and waits for their reply.
- `get_user_request` — delivers the most recent human directive queued for the calling API key.
- `extract_key_info` — chunks caller-provided materials, stores them in PostgreSQL with pgvector, and returns the most relevant contexts for a query.
- `mcp_pipe` — executes a pipeline that composes multiple MCP tools (sequential, parallel, nested) and passes outputs between steps.

Every tool requires a valid `Authorization: Bearer <token>` header. Tokens are also used for billing and for routing questions to the correct user.

## Deployment Prerequisites

Enable the MCP endpoint when starting the API service. The tools are advertised automatically when their dependencies are available.

| Feature            | Requirement                                                                                                                                                                                                                                                                                                                                                           |
| ------------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `web_search`       | Enable at least one engine under `settings.websearch.engines.*` (for example set `settings.websearch.engines.google.enabled` to `true` along with `api_key` and `cx`). Billing is performed against the token owner via `oneapi.CheckUserExternalBilling`.                                                                                                            |
| `ask_user`         | PostgreSQL connection info under `settings.db.mcp` (`addr`, `db`, `user`, `pwd`). The service runs database migrations automatically using GORM.                                                                                                                                                                                                                      |
| `get_user_request` | Same `settings.db.mcp.*` configuration. Stores directives in the `mcp_user_requests` table keyed by the caller’s token hash **and** `task_id`. Retention is controlled by `settings.mcp.tools.user_requests.retention_days` (default `30`) and pruned by a background worker every `settings.mcp.tools.user_requests.retention_sweep_seconds` (default `21600` / 6h). |

If no tool dependencies are met the server skips MCP initialisation.

| Feature            | Requirement                                                                                                                                                           |
| ------------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `extract_key_info` | `settings.db.mcp.*` connection info, `settings.openai.embedding_model`, and `settings.mcp.tools.extract_key_info.enabled=true`. Requires pgvector-enabled PostgreSQL. |
| `mcp_pipe`         | No additional infrastructure dependencies. It is enabled/disabled via `settings.mcp.tools.mcp_pipe.enabled` and can call only tools that are enabled and configured.  |
| `file_*` and RAG memory | Only the binary and the `settings.db.mcp.*` PostgreSQL database (pgvector for semantic search); Redis is not used. The index worker receives the caller's key as an encrypted, expiring envelope in the `kv` table, created by the FileIO startup migration. Semantic indexing also needs `settings.mcp.tools.memory.plugins.rag.security.encryption_keks`; without it, files are indexed lexically. |

## Authentication Model

> **Upgrade note (PR #95).** A key is a tenant only while one-api accepts it. Keys that are unknown, revoked, expired, disabled, quota-exhausted or subnet-restricted now get `401` on every entrypoint, including the free file, memory and user-request tools, and their stored data is unreachable until the key is active again (the data itself is kept, under the same key hash). Clients that read files or request history with a depleted key must top it up first. This is a deliberate security policy, recorded with its rationale and revisit triggers in [`docs/requirements/mcp_credential_policy.md`](../requirements/mcp_credential_policy.md); the design is [`docs/arch/mcp_auth.md`](../arch/mcp_auth.md).

### Bearer Tokens

Both tools rely on a Bearer token supplied via the `Authorization` header. The backend accepts two formats:

1. `Authorization: Bearer <token>`.
2. `Authorization: Bearer <identity>@<token>` — the legacy format. Only `<token>` is used; the `<identity>` part (including a `user-id:ai-id` pair) is accepted for backward compatibility and ignored.

Only the token decides who the caller is:

- The token is validated with one-api (see below).
- Its SHA-256 hash is the tenant namespace. The user and AI identities are both derived from that hash, so `sk-example` and `workspace@sk-example` are the same tenant.
- The last four characters are kept for display in the ask_user dashboard.

A key longer than 256 bytes, or an `Authorization` header longer than 2 KiB, is refused as invalid before it is hashed, cached or sent to one-api.

Store tokens securely; the server does not persist them in plain text.

### Access Boundary

Four separate checks decide what a request may do:

| Responsibility | Decided by | Outcome when it fails |
| --- | --- | --- |
| Protocol discovery | Nobody: open to anonymous callers. | — |
| Authenticated execution | The transport's bearer gate, before the MCP SDK runs. | `401` (no or invalid key) or `503` (no verdict available). |
| Tenant authorization | The verified key's hash, inside each tool. A tool only reads and writes its own tenant's files, memories and requests. | Tool error. |
| Billing authorization | Each paid tool's one-api consume call. | Tool error; the billing outcome is recorded in the call log. |

**Anonymous callers.** A request without any credential may only discover the server. This includes an `Authorization: Bearer` header with an empty token. The allowed JSON-RPC messages are:

- `initialize` and `server/discover`;
- `ping`;
- `tools/list`, `prompts/list`, `resources/list` and `resources/templates/list`;
- `notifications/initialized` (the legacy handshake), without an `id`.

Everything else gets `401` with `WWW-Authenticate: Bearer resource_metadata="<public origin>/.well-known/oauth-protected-resource"`, before the MCP SDK parses the request. That includes `tools/call`, `tasks/*`, `completion/complete`, `resources/read`, `prompts/get`, `logging/setLevel`, `subscriptions/listen`, every other notification (`notifications/cancelled` in particular), client responses, batches, and bodies that are not a single JSON-RPC object.

- The header carries no `error` code, because the request presented no credential (RFC 6750 §3.1).
- The JSON body is `{"error":"authentication_required", "error_description":"…", "resource_metadata":"…"}`.
- MCP clients react to this `401` by asking for credentials. A `200` tool error would not trigger that.

Requests without a body keep reaching the SDK: legacy SSE `GET` streams and session `DELETE`.

**Second check at dispatch.** The MCP SDK also refuses every tool handler for a request whose key the gate did not verify, so a future transport path cannot run a tool anonymously by accident.

- It applies to every tool the server registers, also when a client runs one as a task, and to per-session tools.
- The SDK does not wrap handlers added with `AddTaskTool`. The server adds none, and the test below fails if the advertised catalog ever differs from the registered one.
- The check answers with an `authentication_required` tool error and logs a WARN, because reaching it means a transport path skipped the gate.
- `TestToolDispatchRequiresVerifiedCallerForEveryTool` drives every registered tool through the SDK dispatcher to prove it.

**Cancellation.** Clients cancel a 2026-07-28 request by closing its response stream, as that protocol revision requires; the server then cancels the request. A POSTed `notifications/cancelled` from a 2026-07-28 client is answered `202` and ignored.

- That transport defines no client-to-server notifications.
- The MCP SDK would key such a cancellation by request id alone, because those requests have no session. It would then abort whichever caller's request carried that id.
- The SDK dispatches notifications before it checks the `Mcp-Method` header against the body, so the transport classifies the whole body, decoded exactly as the SDK decodes it: case-folded or escaped `method` keys and duplicate keys (the last one wins) cannot hide a cancellation, nor can a `method` placed after a large `params` value. A batch array is not a single message; the SDK refuses it as a parse error and cancels nothing.
- The policy is one isolated function of the protocol header and the body (`internal/mcp/server_ingress_cancellation.go`), with its own table test (`TestModernCancellationPolicyClassifiesLikeTheSDK`).
- Legacy sessions keep `notifications/cancelled`, scoped to their credential-bound session id.

### Credential Validation

Every entrypoint that reads an API key — the MCP transport, the `/tools/*/api` console APIs, the ask_user console, the GraphQL fields shared with MCP tools, GraphQL `ArweaveUpload` with an API key, the balance proxy and `ValidateOneapiAPIKey`, and Telegram's key linking — authenticates it through one authenticator (`internal/mcp/auth`). A tenant exists only for a key that one-api accepted; a key that is merely non-empty never becomes a tenant. The full inventory, and the test that keeps it complete, is in [`docs/arch/mcp_auth.md`](../arch/mcp_auth.md).

- **Authority.** The key is sent to one-api's read-only `GET /api/token/balance` (behind one-api `TokenAuth`, no billing side effect), under `base_url`. The deprecated `settings.oneapi.balance_url` is still honored when `base_url` is unset, as the exact endpoint URL it always was (a proxy path such as `/custom/balance` keeps working); startup logs a deprecation WARN. A custom path names no one-api base, so consumes then cannot refresh verdicts (`consumes_refresh_verdicts=false`).
  - A key is accepted only on `200` with `Content-Type: application/json` and a bounded body carrying `"success": true`.
  - `401` and `403` reject the key.
  - Everything else means one-api gave no verdict: `429`, `5xx`, redirects, transport errors, timeouts, and also `200 {"success": false}` or an HTML page.
  - Redirects are never followed, whichever HTTP client the validator was built with (`library/httpclient.WithoutRedirects`). The key is only ever sent to the configured authority. Go's own redirect policy would keep the `Authorization` header on a subdomain, on another port or on an `https`→`http` downgrade of the same host. The paid-tool consume call (`POST /api/token/consume`) follows the same rule.
- **MCP transport responses.** A presented but rejected key receives `401` with `WWW-Authenticate: Bearer error="invalid_token", error_description="…", resource_metadata="<public origin>/.well-known/oauth-protected-resource"` before any session is issued or any tool runs. If no verdict can be obtained, the response is `503` with `Retry-After: 5`, never `401` and never a tenant.
- **Protected-resource metadata URL.** Every Bearer challenge (MCP transport `401`, and the agent probes at `/api`, `/api/v1`, `/v1`, `/v2` and `/agent/auth`) advertises the RFC 9728 document of `settings.mcp.public_origin` (default `https://mcp.laisky.com`). The URL comes from configuration only, never from a request's `Host` or `X-Forwarded-Host`.
  - The value must be a bare `http(s)` origin (no path, query, fragment or user info); anything else fails startup.
  - RFC 9728 §3.3 makes clients discard a metadata document whose `resource` differs from that origin. The shipped document is `web/public/.well-known/oauth-protected-resource` (and the other `.well-known` files name the same origin). A self-hosted deployment must replace those files and set the origin; startup logs a WARN when the built document disagrees with the setting.
- **Console and GraphQL responses.** Console APIs keep their `{"error": "..."}` JSON with `401`, or `503` when no verdict is available. GraphQL keeps its normal error path.
- **Anonymous discovery.** Requests without any credential may only discover the server; every other method gets `401` before the MCP SDK runs. See [Access Boundary](#access-boundary).
- **One validation per request.** The verified identity travels in the request context, so nested `mcp_pipe` steps and every handler reuse it.
- **Caching.**
  - An accepted key is trusted for `positive_ttl` (default 5 minutes); this is also the revocation lag.
  - A rejected key is refused for `negative_ttl` (default 30 seconds) without asking one-api again.
  - Concurrent lookups of the same key share one upstream call.
  - A key stays *known* for 24 hours after its last acceptance; this decides whether its revalidation may use the reserved budget below.

#### One-api upstream gate

Every call this server makes to one-api's per-IP-limited REST API on behalf of a presented key goes through one gate (`mcpauth.UpstreamGate`). That covers API key validation and revalidation from every entrypoint above, the `POST /api/oneapi/balance` proxy, and the GraphQL `ValidateOneapiAPIKey` query. The only other one-api REST call, the consume (`POST /api/token/consume`) that every tool call sends, is counted in the same per-IP window (see **One-api window** below).

- A call is charged only when it is about to start: after it holds one of the `max_concurrent` slots. A caller that times out waiting for a slot never contacts one-api and spends no budget. A caller whose budget is already spent is refused at once, without queuing for a slot. Cache hits, negative-cache hits and coalesced waiters are free.
- Balance answers are reused per key hash for 30 seconds. A balance call also records the key's validation verdict, so `ValidateOneapiAPIKey` and the MCP entrypoints share one cache.

The gate's budgets:

- **Per-source budget.** Each client source may start `per_source_burst` new-key or balance calls (default 5), refilled at `per_source_rate_per_second` (default 0.1, i.e. one per 10 seconds).
  - A source is an IPv4 address or an IPv6 `/64`, using `clientip.NetworkKey`.
  - Sources are tracked in an LRU bounded by `max_sources` (default 10,000).
  - A source over its budget gets `503` with `Retry-After` without spending the global budget.
- **Global budget.** All sources together are limited to `upstream_rate_per_second` (default 1) with `upstream_burst` (default 30), and to `max_concurrent` (default 8) calls in flight. This protects one-api's per-IP limit.
- **Reserved revalidation budget.** A known key whose trust period ended revalidates from a reserve that unknown-key and balance traffic cannot spend: `revalidation_rate_per_second` (default 0.2), with `revalidation_burst` (default 20).
  - It falls back to the per-source and global budgets when the reserve is empty.
  - Each key needs at most one revalidation per `positive_ttl`; see [Capacity](#capacity-and-scaling) for how many active keys that sustains.
  - `0` disables the reserve.
- **Client source.** The source is the identity the request-identity middleware resolves with the trusted-proxy policy (`library/clientip`, see `docs/manual/client_identity.md`). Forwarding headers count only when sent by a configured trusted proxy, so one direct peer that rotates `X-Forwarded-For` stays one source. Handlers served outside gin fall back to the TCP peer. Telegram key linking has no client address; each chat is its own source (`telegram:<uid>`).
- **Gateways.** A legitimate gateway or NAT that serves many users from few addresses shares one source, so with the defaults it can onboard 5 new keys at once and then one every 10 seconds (14 of 100 new users arriving one per second; `TestSharedSourceOnboardingCapacity`). Known keys are unaffected: they revalidate from the reserve or refresh through their consumes. List such ranges under `gateways` to give each its own budget:
  - every address of a range is one source with the configured `rate_per_second` and `burst`, which may not exceed the global budget (a gateway can never take more than all sources share, and should stay below it so other sources keep a share);
  - ranges are matched against the client address the trusted-proxy policy resolved, so an untrusted peer cannot claim a gateway's budget with a forwarding header;
  - startup refuses invalid or overlapping ranges, ranges wider than IPv4 `/16` or IPv6 `/32`, and more than 64 ranges.
- **One-api window.** Besides its own buckets, the gate counts every one-api REST call this process starts within one-api's 3-minute sliding window (`oneapi.CallLedger`), consumes included. It starts no validation, balance or revalidation call once that count reaches `oneapi_rate_limit`, so validation never pushes the process over one-api's limit and consumes keep priority. Such a refusal is local (`503`, no stale grace). `0` (an exempt egress) turns the check off.

Outages and local refusals behave differently:

- **Genuine one-api failures.** A timeout, transport error, `429`, `5xx`, redirect or a non-success `200` lets a key that one-api accepted within `positive_ttl + stale_grace` (default grace 30 minutes) stay usable (stale-if-error). Any other key fails closed with `503`.
- **Outage window.** A failure that is not specific to the presented key (anything above except `500` and `200 {"success": false}`, which one-api uses for per-token failures such as a failed owner lookup) opens a process-wide outage window for 30 seconds. While it is open, known keys within their grace are served without any upstream call, and one caller every 15 seconds probes one-api. Without it, every known key would retry on its own, spend the budgets within seconds, and get `503`s (`TestOutageKeepsEveryKnownKeyWithinGrace`: 100 active keys through a 10-minute outage, no refusals, about one probe per 15 seconds).
  - Any verdict, from a probe, any other validation or an accepted consume, closes the window, so a key revoked during the outage is rejected as soon as one-api answers again.
  - A per-token failure never opens it, so a caller holding a broken token cannot extend other keys' lives.
- **Retry spacing.** After a failure for one key, that key is retried upstream at most every 15 seconds.
- **Local refusals.** A refusal by this server's own budgets never grants stale grace by itself. It only serves a known key while the outage window is open, which takes a real one-api failure within the last 30 seconds. Emptying a budget therefore cannot keep a revoked key alive past `positive_ttl`.
- **Overlapping calls.** A validation and a balance query for one key may overlap. An acceptance is dated when its call started, and it never undoes a rejection recorded after that, so the newer rejection wins.

What the gate guarantees under a flood:

- **One source.** A flood from one source (a single host, even one that rotates spoofed forwarding headers) spends only that source's budget. Other callers' new keys still validate.
- **Many sources.** A flood from many distinct sources, such as a botnet, can exhaust the global budget. During it:
  - keys never seen before get `503` until the flood subsides;
  - keys already known keep revalidating from the reserved budget for as long as that budget covers the number of active keys, so active users stay signed in;
  - balance queries for uncached keys get `503`.

Consequences to know:

- **Exhausted tokens.** one-api's `TokenAuth` rejects quota-exhausted tokens with `401`, and one-api offers no validation route that skips the quota check. Such keys therefore lose access to free tools too (file, memory, user-request tools) until the token is topped up or replaced. Stored data stays intact under the same key.
- **Subnet-restricted tokens.** one-api compares the subnet with *this server's* egress IP, so a token restricted to the end user's network is rejected with `403`, which becomes `401` here. Paid tools already failed the same way at `/api/token/consume`.
- **Token-store failures.** one-api answers its own token-store failures with `401` as well. During such a failure, keys whose positive cache expired are rejected until it recovers. This matches one-api's own behavior for its relay.
- **Rate limits.** one-api's per-IP REST rate limit (`GLOBAL_API_RATE_LIMIT`, default 480 per 3 minutes, a sliding window) covers every `/api` call from this server. Relay calls (`/v1/...`: embeddings, rerank, Responses) are limited per token by one-api (`GLOBAL_RELAY_RATE_LIMIT`) and do not share it.
  - With the defaults, the gate's buckets allow at most `1/s × 180 + 30 = 210` new-key and balance calls plus `0.2/s × 180 + 20 = 56` revalidations per 3 minutes. That leaves at least 214 per 3 minutes for `/api/token/consume` calls (every tool call's charge or zero-cost usage report, MCP and GraphQL alike, plus ArweaveUpload and Telegram uploads).
  - Consume calls are never refused locally, because the tool already ran. When they need more than that remainder, the gate yields (**One-api window** above) instead of competing with them. If consumes alone exceed one-api's limit, one-api answers them with `429`; the billing outcome is then classified as denied (no quota spent) and the paid tool fails.

#### Capacity and scaling

**Active keys revalidate through their own tool calls.** Every MCP tool invocation already sends one consume call to one-api with the caller's key: a paid tool's charge, or the zero-cost usage report of every other tool. one-api serves `/api/token/consume` behind the same `TokenAuth` as `/api/token/balance`. So an accepted consume is a fresh verdict, and the key's trust period restarts from the moment that call started. `Authenticator.BeginAuthorityCall` records it.

- A key that calls at least one tool per `positive_ttl` needs no separate validation call. A flood that spends the gate's budgets therefore cannot lock it out.
- The revocation lag stays at most `positive_ttl` after one-api's last acceptance.
- Only the same proof that validation requires counts: `200` with `Content-Type: application/json` and `"success": true` (`oneapi.ConsumeReceipt.TokenAccepted`). A `200` HTML page from a proxy, or a non-success envelope, still bills as before but refreshes nothing.
- A refused or failed consume changes no verdict. Rejections still come only from the validator, so a consume refused for lack of quota does not lock a key out of free tools.
- This applies only when the consume goes to the validator's own base URL, path prefix included. With the defaults, both are `oneapi.BillingAPI`. A self-hosted `base_url` that differs from it turns the refresh off.
- Startup logs `consumes_refresh_verdicts` to show which case applies.
- `TestIngressActiveKeyStaysServedThroughItsOwnConsumes` (`internal/mcp`) runs this on the production handler: budgets spent, the key still served from its consumes, and `503` again `positive_ttl` after the last one.

The budgets below therefore only have to cover keys that call no tool within `positive_ttl`. Examples are clients that only reconnect and list tools, console users, and GraphQL callers.

Each such key revalidates once per `positive_ttl`. With the defaults, revalidations draw on the reserve (0.2/s) and then the global budget (1/s), together about 1.2 calls/s:

- **Without a flood**, about `1.2 × 300 s ≈ 360` keys can be active at once. New keys and balance queries share the global part of that budget.
- **Under a flood that spends the global budget**, only the reserve remains: about `0.2 × 300 s ≈ 60` known keys.
- **Beyond these numbers**, revalidations are refused as unavailable (`503`, never `401`). A local refusal never grants stale grace, so users see intermittent `503`s.
- `TestDefaultBudgetsCapacityForActiveKeys` (`internal/mcp/auth`) simulates these cases on a fake clock: 300 keys stay served without a flood and 50 under one; 150 under a flood exceed the reserve. A one-hour sweep of the same simulation measured the limits at 360 keys without a flood (380 already saw refusals) and 60 under a flood (70 saw refusals).

To support more keys:

- raise `revalidation_rate_per_second`, staying within one-api's per-IP limit (`GLOBAL_API_RATE_LIMIT`, default 480 per 3 minutes, about 2.67/s, shared with billing calls; startup refuses a gate above `oneapi_rate_limit`); or
- raise `positive_ttl`, which lengthens the revocation lag; or
- exempt this server's egress address from one-api's per-IP limit (or raise one-api's limit) and set `oneapi_rate_limit` to match (`0` when exempt).

Before treating the defaults as safe for a deployment, compare its real number of simultaneously active keys with these limits:

- **Configured capacity.** At startup, the `MCP API key validation enabled` line logs `active_key_capacity`, `active_key_capacity_under_flood` and `max_oneapi_calls_per_3m` for the configured settings. These are the figures above, per process.
- **Observed demand.** The call log gives an upper bound. Keys that called tools refresh through their consumes, so they need less than this. Query, for the busiest 5-minute windows of the last 30 days (PostgreSQL 14+):

  ```sql
  SELECT date_bin('5 minutes', occurred_at, TIMESTAMPTZ '2000-01-01') AS window_start,
         count(DISTINCT api_key_hash) AS active_keys
  FROM mcp_call_logs
  WHERE occurred_at >= now() - interval '30 days' AND api_key_hash IS NOT NULL AND api_key_hash <> ''
  GROUP BY 1 ORDER BY 2 DESC LIMIT 10;
  ```

- **Exhaustion signal.** The `api key validation unavailable` lines carry `unavailable_cause`:
  - `local_budget` when the local budgets ran out;
  - `authority` when one-api gave no verdict;
  - `local` for a request that ended while waiting, or a missing authenticator. A request that ended while waiting never receives stale grace.

  Each cause gets its own WARN summary per minute, so a capacity shortage stays visible during an outage. The same causes are counted in the metrics below.

#### Monitoring

The authenticator exports, on `/metrics`:

| Metric | Meaning |
| --- | --- |
| `mcp_auth_decisions_total{result}` | One per authentication: `accepted_cache`, `accepted_authority`, `accepted_stale`, `rejected_cache`, `rejected_authority`, `invalid`, `unavailable_local_budget`, `unavailable_authority`, `unavailable_local`. |
| `mcp_auth_gate_refusals_total{budget}` | Calls the gate refused, by the budget that refused them: `per_source`, `global`, `oneapi_window`, `concurrency`. |
| `mcp_auth_authority_calls_total{class,verdict}` | Calls the gate started (`new_key`, `revalidation`, `balance`) and one-api's verdict (`accepted`, `rejected`, `no_verdict`). |
| `mcp_auth_known_key_rejections_total` | Rejections of keys one-api had accepted before: revocations, exhausted quota, or one-api's own token-store failures. |
| `oneapi_rest_calls_total{route}`, `oneapi_rest_calls_in_window` | one-api REST calls this process started (`token_balance`, `token_consume`), and how many fall in the current 3-minute window. |
| `mcp_auth_oneapi_rate_limit` | The configured `oneapi_rate_limit` (`0` = exempt). |

Suggested alerts:

- **one-api outage:** `rate(mcp_auth_decisions_total{result=~"accepted_stale|unavailable_authority"}[5m]) > 0`.
- **Capacity:** `rate(mcp_auth_decisions_total{result="unavailable_local_budget"}[5m]) > 0`; `mcp_auth_gate_refusals_total` names the budget to raise or the flood to look for.
- **Shared one-api limit:** `oneapi_rest_calls_in_window / mcp_auth_oneapi_rate_limit > 0.8` (when the limit is not `0`).
- **Token-store failure or mass revocation:** a spike of `rate(mcp_auth_known_key_rejections_total[5m])`. one-api answers its token-store failures with `401`, which this server cannot tell from a revocation.

Revocation and failure:

- A revoked key keeps working for up to `positive_ttl` after one-api's last acceptance (dated when the accepting call started).
- During a genuine one-api outage, a key accepted before the outage keeps working for up to `positive_ttl + stale_grace` (default 35 minutes).

Several replicas:

- Every budget, cache, concurrency cap and the one-api window ledger are local to one process.
- Replicas behind a shared egress address therefore add up at one-api: `N` replicas can send `N` times the configured rates.
- Divide `upstream_*`, `revalidation_*`, `max_concurrent` and `oneapi_rate_limit` by the replica count, or route one-api calls through one egress per replica.
- A shared limiter would avoid that division. An example is a Redis GCRA keyed by egress address, with a local fallback of budget/N when Redis is unreachable. It is not built in, because it makes Redis a hard dependency of every authentication. Consider it only if replicas scale dynamically.

#### Prod mode

`enabled: false` is accepted only when the server runs with `--debug`. In prod mode (without `--debug`), startup fails with a configuration error instead of accepting every bearer. Development hosts without one-api run with `--debug`.

Settings (`settings.mcp.auth.validation.*`). For `stale_grace` and `revalidation_rate_per_second`, an explicit `0` disables the feature; an unset key keeps the default. Other zero or negative values fall back to the default or the minimum shown.

| Key                        | Default                    | Meaning                                                      |
| -------------------------- | -------------------------- | ------------------------------------------------------------ |
| `enabled`                  | `true`                     | One-api validation. `false` is a development-only opt-out: refused at startup without `--debug`, and with `--debug` it logs a WARN and accepts any non-empty bearer. |
| `base_url`                 | `oneapi.BillingAPI`        | one-api origin; the endpoint is `base_url` + `/api/token/balance`. When unset, the deprecated `settings.oneapi.balance_url` is used verbatim as the endpoint. Startup refuses a URL that is not absolute `http(s)` or carries user info. |
| `timeout`                  | `5s` (100ms–30s)           | Upstream timeout per call, including the wait for a slot.    |
| `positive_ttl`             | `5m` (1s–1h)               | Trust period for an accepted key (revocation lag).           |
| `negative_ttl`             | `30s` (1s–5m)              | Refusal period for a rejected key.                           |
| `stale_grace`              | `30m` (0–24h, `0` disables) | Extra period during which an accepted key survives a genuine one-api failure. |
| `max_entries`              | `10000` (at most 1,000,000) | Cardinality bound of each key cache.                        |
| `max_concurrent`           | `8` (at most 256)          | Concurrent upstream calls.                                   |
| `upstream_rate_per_second` | `1`                        | Global rate of new-key and balance calls.                    |
| `upstream_burst`           | `30`                       | Global burst of new-key and balance calls.                   |
| `per_source_rate_per_second` | `0.1`                    | Refill rate of each client source's budget.                  |
| `per_source_burst`         | `5`                        | Calls one client source (IPv4 address or IPv6 /64) may start at once. |
| `max_sources`              | `10000` (at most 1,000,000) | Client sources tracked (least recently used evicted).       |
| `revalidation_rate_per_second` | `0.2` (`0` disables)   | Reserved rate for revalidating known keys.                   |
| `revalidation_burst`       | `20`                       | Reserved burst for revalidating known keys.                  |
| `oneapi_rate_limit`        | `480` (`0` = egress exempt) | This process's share of one-api's per-IP `GLOBAL_API_RATE_LIMIT`, in calls per 3 minutes. Startup refuses a gate whose buckets may start more calls than this (the per-3-minute total under **Rate limits** above); at run time the gate starts no call once this process's one-api REST calls in the window, consumes included, reach it. |
| `gateways`                 | none                       | List of `{cidr, rate_per_second, burst}`: client ranges with their own per-source budget (see **Gateways** above). |

Startup also refuses budgets that would defeat themselves: any rate that is not finite or exceeds 1000/s, any burst above 100,000, `per_source_rate_per_second` above `upstream_rate_per_second`, and `per_source_burst` above `upstream_burst` (one source could otherwise spend the whole global budget). These checks apply while validation is enabled.

`settings.mcp.public_origin` (default `https://mcp.laisky.com`) is the public origin whose `/.well-known/oauth-protected-resource` every Bearer challenge advertises.

Logging: rejected bearers are logged at DEBUG. Unavailable verdicts produce at most one WARN summary per minute, per entrypoint and per `unavailable_cause` (`local_budget`, `authority` or `local`), with a suppressed-count field, plus DEBUG lines per request. Stale-grace answers produce one WARN summary per minute. Anonymous requests refused at the transport are logged at DEBUG with their JSON-RPC method.

### Request Size and Time Limits

Request bodies are budgeted per route family before any middleware or handler reads them.

- **Byte cap.** A declared `Content-Length` above the cap is refused with `413` before a byte is read or a credential is validated. Bodies without a declared length are capped while streaming.
- **Minimum data rate.** A client gets `read_grace` for its first bytes and must then keep delivering at least `min_data_rate` bytes per second. Every byte earns `1/min_data_rate` seconds of read time. Large uploads over slow but live links therefore succeed, while a stalled or trickling (slow-loris) body is cut.
- **Bounded credit.** Bytes delivered faster than the minimum rate bank at most `max_idle` of read time. A fast client may therefore pause up to `max_idle`. A burst cannot fund a later stall or a trickle below the minimum rate, which a cumulative rate (Kestrel's `MinRequestBodyDataRate`, Apache's `MinRate`) would allow: 1 MiB at once would otherwise buy about 4.5 minutes of silence. A plain inter-read timer (nginx `client_body_timeout`) would still admit a trickle. Over any interval, a live body delivers at least `min_data_rate × (interval − max_idle)` bytes.
- **Absolute cap.** The whole read is limited to the smaller of `max_read_duration` and `read_grace + cap / min_data_rate`.
- **In-flight body budgets (MCP transport).** The per-request cap does not stop many concurrent requests from each holding a large body in memory: the MCP SDK reads every POST body whole, and the transport buffers 2026-07-28 and anonymous bodies whole first to classify them. So the body bytes in-flight MCP requests hold together are bounded by two budgets:
  - **Verified:** requests whose API key the credential gate verified share `mcp_max_inflight_bytes` (default 4 × `mcp_max_bytes` = 128 MiB). One key (its tenant hash) may hold at most a quarter of it, and never less than `mcp_max_bytes` (default 32 MiB), so one caller cannot starve the others.
  - **Anonymous:** requests without a credential (discovery only) share `mcp_anonymous_max_inflight_bytes` (default 8 × `mcp_anonymous_max_bytes` = 8 MiB), with no per-caller share. Credential-less clients therefore cannot spend the verified budget, however many connections they open.
  - Each budget is never below its per-request cap, so one body at the cap always fits an idle budget.
  - **Classification first.** The body budget runs before authentication and charges nothing yet: before the credential gate classifies a request, it may read at most 64 KiB (the request logger reads a 4 KiB preview). The gate then assigns the body to the anonymous budget, or to the verified one under the verified key. A request whose bearer is rejected, malformed or cannot be verified is never assigned, and its preview bytes are released when it ends.
  - **Bytes actually read.** Every assigned body is charged exactly the bytes it has delivered, for declared and chunked bodies alike. A declared `Content-Length` that has not arrived costs nothing, so slow or stalled senders cannot reserve capacity they do not use. The transport's buffers grow with the received bytes, never to a declared length up front, so the charge follows memory. A declared length above the byte cap is still refused with `413` before any byte is read.
  - **Refusal.** When the bytes do not fit (at classification, or at a later read), the read fails. Where the transport answers itself (classification, and the buffered 2026-07-28 and anonymous POSTs), the request gets `503` with `Retry-After: 2`, `Connection: close` and `{"error":"temporarily_unavailable", …}`. A legacy-session POST, which the SDK reads directly, gets the SDK's JSON-RPC parse error instead.
  - The charge is released when the request's handler returns, so a long tool call keeps its body charged while it runs.
  - The budgets count body bytes, not heap. While a 2026-07-28 request runs, the transport's copy and the SDK's own copy coexist, and growing buffers over-allocate, so the heap they use is about 2–3 × the charged bytes. Size the budgets against the memory reserved for request bodies.
  - **Metrics.** `http_request_body_inflight_bytes{entry}` (bytes read and not yet released), `http_request_body_inflight_capacity_bytes{entry}` and `http_request_body_inflight_refused_total{entry,reason}`, with `entry` = `mcp` (verified) or `mcp_anonymous`, and `reason` = `capacity` (the budget is full) or `tenant_share` (one key holds its share). `http_request_body_unclassified_refused_total` counts requests that read more than 64 KiB before classification, which the shipped handler chain never does. Alert on a sustained increase of `http_request_body_inflight_refused_total{entry="mcp"}`, or on `http_request_body_inflight_bytes / http_request_body_inflight_capacity_bytes` staying above 0.8; a rising `reason="tenant_share"` names one heavy key rather than an overload.
- **Bodyless requests.** Requests without a body (SSE `GET` streams, held long polls after their body was read, websockets) are never limited.
- **Protocols and fallback.** The data-rate rule arms a per-request read deadline through `http.ResponseController`, which works on HTTP/1.1 and on HTTP/2 streams (both covered by real-socket tests). If a deadline cannot be armed, for example behind a `ResponseWriter` wrapper without `Unwrap`, the request still runs with its byte cap only. Each such request is counted in `http_request_body_read_deadline_unarmed_total{entry="mcp|graphql|tool_console|default"}` and logged at WARN at most once a minute per entry (`suppressed_since_last` counts the rest). Alert on any increase: slow-body protection is off on that entry.

Settings (`settings.web.request_body.*`, plus `settings.web.idle_timeout`):

| Key                       | Default   | Applies to                                                                                     |
| ------------------------- | --------- | ---------------------------------------------------------------------------------------------- |
| `mcp_max_bytes`           | 32 MiB    | MCP transport requests that carry a validated key. This leaves room for the largest tool call, `extract_key_info` with 10 MB of materials. |
| `mcp_anonymous_max_bytes` | 1 MiB     | MCP transport requests without credentials (discovery only, see [Access Boundary](#access-boundary)). |
| `mcp_max_inflight_bytes`  | 4 × `mcp_max_bytes` (128 MiB) | Body bytes actually read that in-flight verified MCP requests may hold together; one key at most a quarter (never below `mcp_max_bytes`). Never below `mcp_max_bytes`. Beyond it, a retryable `503` (see above). |
| `mcp_anonymous_max_inflight_bytes` | 8 × `mcp_anonymous_max_bytes` (8 MiB) | The same for MCP requests without credentials, with no per-caller share; never below `mcp_anonymous_max_bytes`. |
| `graphql_max_bytes`       | 64 MiB    | `/query` and `/query/v2`. gqlgen's multipart upload limit (32 MiB) still applies inside.        |
| `default_max_bytes`       | 1 MiB     | Every other route (one-api balance proxy, SSO code exchange, webhooks). Routes with tighter own caps keep them. |
| `min_data_rate`           | `4096` B/s | Minimum delivery rate after the grace period, on every route.                                 |
| `read_grace`              | `10s`     | Time before the first bytes must arrive.                                                       |
| `max_idle`                | `30s`     | Longest pause after bytes arrived, and the most read time a burst can bank (never below `read_grace`). |
| `max_read_duration`       | `30m`     | Absolute cap of one body read. A 102 MiB image upload needs about 0.5 Mbit/s to finish within it. |
| `idle_timeout`            | `120s`    | Keep-alive idle connections (`http.Server.IdleTimeout`).                                         |

The `/tools/<name>/api` console routes keep their own byte caps (for example user-request image uploads, `per_image_max_bytes × max_per_request + 2 MiB`). They get the data-rate rule with `max_read_duration` as the absolute cap. The server sets `ReadHeaderTimeout` (10s) and `IdleTimeout`. It intentionally sets no `ReadTimeout` or `WriteTimeout`, because those would apply to SSE streams and long polls.

## HTTP Endpoints

| Path                                             | Methods                 | Purpose                                                                                                       |
| ------------------------------------------------ | ----------------------- | ------------------------------------------------------------------------------------------------------------- |
| `/mcp`                                           | `POST`, WebSocket       | Primary MCP transport, handled by the streamable MCP server.                                                  |
| `/mcp/debug`                                     | `GET`, `HEAD`           | Serves a pre-configured MCP Inspector page. Optional `?endpoint=` and `?token=` query params allow overrides. |
| `/mcp/tools/ask_user`                            | `GET`                   | Web console for human operators. Prompts for the API key and then shows pending questions and history.        |
| `/mcp/tools/ask_user/api/requests`               | `GET`                   | JSON API used by the console. Requires the same `Authorization` header.                                       |
| `/mcp/tools/ask_user/api/requests/{id}`          | `POST`                  | Submits the human response for a pending request.                                                             |
| `/mcp/tools/get_user_requests`                   | `GET`                   | React console that lets humans queue, review, and delete directives for `get_user_request`.                   |
| `/mcp/tools/get_user_requests/api/requests`      | `GET`, `POST`, `DELETE` | Lists, creates, or bulk-deletes user directives scoped to the bearer token.                                   |
| `/mcp/tools/get_user_requests/api/requests/{id}` | `DELETE`                | Removes a single directive.                                                                                   |
| `/mcp/tools/get_user_requests/api/preferences`   | `GET`, `PUT`, `POST`    | Reads/updates per-user MCP preferences (`return_mode`, `disabled_tools`) and returns `available_tools`.       |

> **Note:** The console endpoints are intended for browsers. They are protected only by the bearer token, so deploy behind HTTPS and avoid exposing them publicly without additional access controls.

## Tool Reference

### `web_search`

- **Description:** Search the public web using Google Programmable Search and return structured results.
- **Input Parameters:**
  - `query` (string, required) — plain text search phrase.
- **Behaviour:**
  1. Validates the token and charges the user via `oneapi.CheckUserExternalBilling` (`PriceWebSearch`).
  2. Issues the request to the configured Google search engine.
  3. Returns a JSON payload compatible with `search.SearchResult`.
     > **Cost:** Google currently charges roughly USD $5 per 1,000 queries on the Custom Search JSON API.
- **Sample Result:**

```json
{
  "query": "mcp protocol overview",
  "created_at": "2025-10-23T18:24:26Z",
  "results": [{ "url": "https://example.com", "name": "Example", "snippet": "..." }]
}
```

- **Error Conditions:**
  - Missing/invalid token → `missing authorization bearer token`.
  - Billing refusal → `billing check failed: ...`.
  - Empty search phrase → `query cannot be empty`.

### `ask_user`

- **Description:** Ask the authenticated human for additional information and wait for their reply.
- **Input Parameters:**
  - `question` (string, required) — the message presented to the user.
- **Behaviour:**
  1. Parses the token to determine user/AI identities and a hashed key.
  2. Creates a `pending` request in PostgreSQL.
  3. Polls until the request is answered, cancelled, or times out (five minutes).
  4. Returns the human’s response when available.

- **Response Shape:**

```json
{
  "request_id": "f5f7c1cd-8573-41c4-9c8e-43bcc21ede35",
  "question": "Please approve deployment?",
  "answer": "Approved. Deploy at 18:00 UTC.",
  "asked_at": "2025-10-23T18:20:00Z",
  "answered_at": "2025-10-23T18:21:42Z"
}
```

- **Timeouts & Errors:**
  - If the human does not respond within five minutes the request is marked `expired` and the tool returns `timeout waiting for user response`.
  - Invalid or missing tokens → `invalid authorization header`.
  - Requests that are already closed return a descriptive error.

#### Human Console Workflow

1. Open `/mcp/tools/ask_user` in a browser.
2. Enter the same API key used by the AI.
3. Review the pending questions list. Each entry shows the ID, timestamp, and originating AI.
4. Enter a reply and submit. The answer is stored, the status flips to `answered`, and the AI receives the response immediately.
5. The history section lists previously answered or expired requests for audit purposes.

The console stores the API key locally (browser `localStorage`) so it can resume polling automatically. Clear the key using the form to stop receiving updates.

### `get_user_request`

- **Description:** Deliver the most recent human-authored directive waiting for the authenticated API key and immediately mark it as consumed so it is not replayed. Data is isolated by the combination of hashed API key and `task_id`.
- **Input Parameters:**
  - `task_id` (string, optional) — isolates queues for concurrent agent tasks. Defaults to `default` and is normalised to a safe identifier shared across MCP tools.
- **Behaviour:**
  1. Parses and validates the bearer token, deriving `user_identity` and the hashed key.
  2. Normalises `task_id` (defaults to `default`) and looks up the newest `pending` entry in `mcp_user_requests` for that key **and task**.
  3. Atomically flips the entry to `consumed`, stamps `consumed_at`, and returns the payload to the caller.
  4. When the queue is empty the tool responds immediately with `{ "status": "empty" }` instead of raising an error.
  5. A background worker prunes requests older than the configured retention window so stale directives do not leak across sessions.
- **Response Shape:**

```json
{
  "request_id": "22222222-2222-2222-2222-222222222222",
  "content": "Ship release v2 after the smoke tests finish.",
  "task_id": "default",
  "status": "consumed",
  "created_at": "2025-10-23T18:20:00Z",
  "consumed_at": "2025-10-23T18:21:42Z",
  "user_identity": "user:workspace",
  "key_hint": "9f42"
}
```

- **Error Cases:** missing/invalid token (`invalid authorization header`), database outages (`failed to fetch user request`), or context cancellations/timeouts inherited from the MCP caller. Running with an empty queue is treated as a successful call and returns a descriptive JSON payload. Requests older than the retention window are silently pruned.

- **Preferences API (`/api/preferences`):**
  - `GET` returns:
    - `return_mode` (`all` or `first`)
    - `disabled_tools` (string array)
    - `available_tools` (all MCP tools enabled on this server instance)
  - `PUT`/`POST` accepts either field independently:
    - `{ "return_mode": "all" | "first" }`
    - `{ "disabled_tools": ["web_fetch", "file_write"] }`
  - Preferences are isolated by bearer token hash.

- **Tool Activation Behaviour:**
  - Any tool name in `disabled_tools` is treated as user-disabled.
  - MCP `tools/list` responses are filtered per user, so disabled tools do not appear in the advertised tool list for that token.

#### User Requests Console Workflow

1. Open `/mcp/tools/get_user_requests` and authenticate with the same bearer token the AI will use.
2. Draft directives in the editor and optionally label them with a `task_id`. The newest entry is what `get_user_request` returns first. Separate `task_id` values do not interfere with one another for the same API key.
3. Review two columns: `Pending` items are still waiting to be consumed, while `Consumed history` shows everything that has already been delivered.
4. Remove obsolete entries individually or purge everything with the “Delete all” button. Deletions only affect the authenticated API key and task combination and do not change other tenants.

### `extract_key_info`

- **Description:** Chunk arbitrary materials, compute embeddings with the caller's OpenAI-compatible key, and return the top-matching context slices.
- **Input Parameters:**
  - `query` (string, required) — natural-language question.
  - `materials` (string, required) — source text to analyse.
  - `top_k` (int, optional) — number of contexts to return (default `5`, max `20`).
- **Behaviour:**
  1. Validates payload size (`settings.mcp.tools.extract_key_info.max_materials_size`).
  2. Bills the caller via `oneapi.CheckUserExternalBilling` using `PriceExtractKeyInfo`.
  3. Derives `user_id` (hashed token) and `task_id` (prefix before `@`) from the Authorization header.
  4. Splits `materials` into paragraphs (<1500 chars), cleans whitespace, and tokenises for BM25-style scoring.
  5. Embeds each chunk plus the query with the configured model (default `text-embedding-3-small`) and persists to PostgreSQL tables `mcp_rag_tasks`, `mcp_rag_chunks`, `mcp_rag_embeddings`, and `mcp_rag_bm25`.
  6. Executes a hybrid ranking (semantic cosine similarity + keyword overlap) and returns the top `top_k` chunks preserving order.
- **Sample Response:**

```json
{
  "contexts": [
    "The billing service must receive tenant-specific quotas...",
    "Each chunk stores user_id, task_id, and materials_hash metadata..."
  ],
  "count": 2,
  "task_id": "workspace"
}
```

- **Error Cases:** invalid/missing token, payload too large, `top_k` outside allowed range, billing refusal, or upstream embedding failures.
- **GraphQL Equivalent:** the same capability is exposed as the `ExtractKeyInfo` mutation on the GraphQL endpoint, for clients that do not speak MCP. It applies the same validation, billing (`PriceExtractKeyInfo`), and tenant isolation, and reads the bearer token from the request `Authorization` header.

```graphql
mutation {
  ExtractKeyInfo(query: "who owns billing?", materials: "...", top_k: 5) {
    query
    created_at
    contexts
  }
}
```

### `mcp_pipe`

- **Description:** Execute a processing pipeline that composes existing MCP tools in a single call.

  Typical use is chaining the built-in tools:
  1. `web_search` → get URLs
  2. `web_fetch` → fetch page content
  3. `extract_key_info` → extract the relevant contexts

  `mcp_pipe` also supports parallel groups and nested pipelines.

- **Input Parameters:**

  `mcp_pipe` accepts either:
  1. `spec` (string or object, optional) — a pipeline specification, or
  2. the pipeline spec object passed directly as the tool arguments.

  The pipeline spec schema is:
  - `vars` (object, optional): user-defined variables.
  - `steps` (array, required): ordered list of steps.
  - `return` (any, optional): a selector for the final return value. Defaults to the last step result.
  - `continue_on_error` (bool, optional, default `false`): whether to continue executing remaining steps after a failure.

- **Step Types:** each step must have a non-empty `id` and specify exactly one of:
  - Tool step:

    ```json
    { "id": "search", "tool": "web_search", "args": { "query": "${vars.q}" } }
    ```

  - Parallel group:

    ```json
    {
      "id": "pages",
      "parallel": [
        { "id": "p1", "tool": "web_fetch", "args": { "url": "https://a" } },
        { "id": "p2", "tool": "web_fetch", "args": { "url": "https://b" } }
      ]
    }
    ```

  - Nested pipeline:

    ```json
    {
      "id": "child",
      "pipe": {
        "steps": [{ "id": "f", "tool": "web_fetch", "args": { "url": "https://x" } }],
        "return": { "$ref": "steps.f.structured.content" }
      }
    }
    ```

- **Passing Outputs to Later Steps:**

  `mcp_pipe` resolves inputs using:
  1. String interpolation: replace `${...}` inside strings.
  2. Typed references: `{ "$ref": "path.to.value" }` which preserves the referenced value's type.

  Available roots:
  - `vars` — from `spec.vars`
  - `steps` — per-step results populated during execution
  - `last` — last executed step result

  Array indices are numeric path segments (e.g. `...results.0.url`). Parallel group child results are stored under `steps.<group_id>.children.<child_id>`.

- **Sample: Search → Fetch → Extract**

  ```json
  {
    "vars": { "q": "mcp protocol overview" },
    "steps": [
      {
        "id": "search",
        "tool": "web_search",
        "args": { "query": "${vars.q}" }
      },
      {
        "id": "fetch",
        "tool": "web_fetch",
        "args": { "url": { "$ref": "steps.search.structured.results.0.url" } }
      },
      {
        "id": "extract",
        "tool": "extract_key_info",
        "args": {
          "query": "${vars.q}",
          "materials": "${steps.fetch.structured.content}"
        }
      }
    ],
    "return": { "$ref": "steps.extract.structured.contexts" }
  }
  ```

- **Sample: Parallel Fetch then Extract**

  ```json
  {
    "vars": { "q": "golang", "u1": "https://a", "u2": "https://b" },
    "steps": [
      {
        "id": "pages",
        "parallel": [
          { "id": "p1", "tool": "web_fetch", "args": { "url": "${vars.u1}" } },
          { "id": "p2", "tool": "web_fetch", "args": { "url": "${vars.u2}" } }
        ]
      },
      {
        "id": "extract",
        "tool": "extract_key_info",
        "args": {
          "query": "${vars.q}",
          "materials": "${steps.pages.children.p1.structured.content}\n${steps.pages.children.p2.structured.content}"
        }
      }
    ],
    "return": { "$ref": "steps.extract.structured.contexts" }
  }
  ```

- **Output Shape:**

  `mcp_pipe` returns a JSON object with:
  - `ok` (bool): overall success.
  - `error` (string): the first error encountered (empty on success).
  - `result` (any): selected return value.
  - `steps` (object): per-step results with timestamps, durations, and captured tool outputs.

  Each tool step includes:
  - `structured`: the sub-tool's structured output (`structuredContent`)
  - `text`: the sub-tool's text output (the JSON string fallback)

- **Error Cases:**
  - Invalid spec (`steps cannot be empty`, invalid `$ref`, duplicate step IDs).
  - Unsupported tool name.
  - Safety limit violations (too many steps, too deep nesting).
  - Sub-tool errors are surfaced as a failed step and propagated (unless `continue_on_error=true`).

- **Billing Notes:**

  `mcp_pipe` itself has no direct billing. Sub-tools still run their usual billing checks (e.g. `web_search`, `web_fetch`, `extract_key_info`) using the caller's bearer token. If the overall pipeline fails, any sub-tools that already completed successfully are still billed (there is no rollback).

- **Configuration:**

  Disable the tool with `settings.mcp.tools.mcp_pipe.enabled=false`.

### Image Messages

`get_user_request` supports image attachments alongside the usual text directives. Submitted attachments ride along with the directive, get normalized server-side, and are surfaced to the AI agent via the MCP response as a combination of inline `ImageContent` (for small images) and `resource_link` blocks (for every image).

- **Enabling the feature**: set `settings.mcp.tools.user_requests.images.enabled: true` and fill in the `minio` subsection with endpoint/bucket/credentials. When disabled (the default) the feature is invisible: the compose UI hides the image controls, multipart POSTs return `415 feature_disabled`, and MCP responses stay byte-identical to the pure-text shape.
- **Attachment intake**: callers either POST `multipart/form-data` with `content`, `task_id`, `images` file parts, and `image_urls` text parts, or keep the JSON variant and provide `image_urls` there. Up to 5 attachments per request (files and URLs combined), 20 MiB raw cap per attachment, 100 MiB per-user quota across live objects.
- **Accepted formats**: `image/jpeg`, `image/png`, `image/webp`, `image/bmp`, `image/tiff`, and the first frame of `image/gif` (only that frame is decoded). `image/svg+xml` and `image/heic` are rejected (`unsupported_mime`). Every stored object is re-encoded as PNG after decode -> orientation fix -> resize to a 1536-pixel longest edge. Either side above 8192 px is rejected (`decode_failed`).
- **Decode memory budget**: before decoding, the server estimates the image's decode + orientation + resize + encode working set from its header and admits it through a process-wide budget, `settings.mcp.tools.user_requests.images.decode_memory_budget_bytes` (default 1342177280 = 1.25 GiB). Every 8-bit-per-channel image of every accepted format up to 8192 × 8192 fits the default, with two exceptions: TIFFs stored as one large compressed strip or tile (always fine up to ~28 MP; above that only if the strip is small) and progressive full-chroma or CMYK JPEG (fine up to roughly 40–60 MP). 16-bit interlaced PNG above roughly 60 MP is rejected too. A budget lowered at start (see below) admits less: an 8192 × 8192 RGBA PNG alone is estimated at about 686 MiB. Rejected images get `decode_failed`; raise the budget to accept them. Admission is fair: each user identity has at most 2 decodes running or queued, and a small image that fits may overtake a large one for up to 2 s, after which the large one is guaranteed to go next. The setting bounds image-decoding memory, not the whole create path: each running create also holds its multipart form (up to 18 MiB), one raw attachment (`per_image_max_bytes`) and every image it already normalized (up to ~18 MiB each) until its uploads end. At start the server logs this *image memory envelope* — decode budget + `max_concurrent_requests` × (18 MiB + `per_image_max_bytes`) + `max_concurrent_requests` × `max_per_request` × 18 MiB, about 2.25 GiB with the defaults — and compares it with the process memory limit (the Go soft limit or the container's cgroup limit, whichever is tighter). When `decode_memory_budget_bytes` is not set, the budget is lowered so the envelope fits half that limit (never below 256 MiB; a WARN says so); an explicitly set budget is kept. An envelope that still exceeds half the limit is an ERROR: lower `max_concurrent_requests` (each create slot accounts for 128 MiB with the defaults) or the budget. Unless `GOMEMLIMIT` is set in the environment, the `api` command sets the Go soft memory limit to 90% of the container memory limit: without it the Go collector let the heap reach about twice the live set (measured peak 2.0–2.1 × the envelope under eight worst-case creates, 1.01 × with the soft limit). Set `GOMEMLIMIT` to choose another value or `GOMEMLIMIT=off` to keep the runtime default. The `/metrics` gauges `mcp_user_image_decode_budget_bytes` (the budget after start-up sizing), `mcp_user_image_decode_in_use_bytes` (estimated working set of the decodes admitted now) and `mcp_user_image_decode_waiting` (decodes waiting for budget or for one of their identity's two slots) show decode admission; compare them with `process_resident_memory_bytes`, since the budget bounds decoding, not the whole process. A sustained non-zero `mcp_user_image_decode_waiting` means creates are queuing for decode budget.
- **Storage & expiry**: images live in MinIO keyed by SHA256 under the configured prefix. A bucket-level lifecycle rule expires objects after 7 days; the same SHA from the same user dedupes and refreshes the TTL. A DB-side GC hourly reaps expired metadata rows.
- **Upload consistency**: at most `max_concurrent_requests` (default 8) image requests run per process and `max_concurrent_requests_per_user` (default 2) per identity; as many more may wait (overall and per identity) and get `image_busy` when their deadline passes, and requests beyond that queue get `image_busy` at once. A multipart request is admitted before its body is read, and its attachments are then read one at a time, so input memory is bounded by `max_concurrent_requests` × (18 MiB + `per_image_max_bytes`) on top of the decode budget (see the image memory envelope above). The body must arrive within the request's 30 s deadline; otherwise the request gets `image_busy`. The same deadline covers normalizing, uploading and committing: the objects of one request are uploaded up to 4 at a time (each PUT at most 20 s) and all uploads must end 2 s before the deadline, which keeps that time for the commit; a request with less than 3 s left for uploading once its images are normalized gets `image_busy` before any quota is reserved or anything is uploaded. Send text-only requests as JSON: a multipart request takes an image slot even without attachments. Each request reserves its quota in a short transaction (recording upload claims in `mcp_user_image_claims`), uploads the objects with no database transaction or connection held, and then commits the request in a second short transaction. When anything fails after the reservation (an upload, the commit, a client disconnect), the server deletes the request's objects unless a committed request shares them, and re-checks every key it started uploading 5 minutes later, because the object store may still apply a PUT the server gave up on. The object store is not transactional: it converges to the database through these records (immediately, after 5 minutes for a late write, after 10 minutes for a crashed request) rather than atomically; the lifecycle rule removes anything applied later still. Concurrent requests of one user are serialized for quota purposes by a PostgreSQL advisory lock on the user identity, which does not depend on the key prefix, so instances with different prefixes during a configuration change still share it (at READ COMMITTED whatever the database default). The quota charges each image once: a second request that attaches an image another request of the same user is still uploading is not charged again, and the image stays charged while any of those requests still holds it. While a delete of an image is still in flight, attaching the same image again returns `image_busy` for up to 5 minutes.
- **Orphan reconciler**: an hourly tick (`orphan_sweep_seconds`, default 3600; 0 disables) processes due claims in passes of up to `orphan_sweep_batch` (default 1000) — uploads of requests that crashed, deletes that failed, refs the GC expired, late-write re-checks of failed requests — and deletes the object unless an image ref or an in-flight request still uses it, with at most 4 deletes in flight. A pass that fills its batch is followed by another, up to 8 passes (5 minutes each at most) per tick, so a backlog drains at up to 8 batches per interval. A key whose delete fails keeps its 5-minute delete lease and is retried after it expires, so later passes of the same tick work on other claims; only a pass that settles no key at all (the store or the database is down) ends the tick early. Shutdown stops a tick between passes. It never lists the bucket: it only deletes keys this database recorded writing, so objects written by other deployments or before this release are never touched (the lifecycle rule handles those). Disabling it leaves expired objects to the lifecycle rule, and the GC then purges stale claims after one object TTL; while it is enabled, claims are kept until it processes them. When the oldest due claim has waited for more than 24 hours, the reconciler logs `image object cleanup is falling behind` at ERROR (at most every 6 hours).
- **Cleanup metrics** (Prometheus, served at `/metrics` once the image subsystem starts; a process with images disabled exports none): `mcp_user_image_claims_due` and `mcp_user_image_claims_oldest_due_age_seconds` (backlog left after the last tick), `mcp_user_image_object_deletes_total{path="compensation|reconciler",result="deleted|failed"}`, `mcp_user_image_reconcile_passes_total{result="ok|error"}`, `mcp_user_image_reconcile_last_success_timestamp_seconds`, `mcp_user_image_lifecycle_rule_present` (1 while the last lifecycle check found or restored the rule), `mcp_user_image_lifecycle_reinstalls_total` (checks that found the rule erased by another writer) and `mcp_user_image_reconciler_enabled`. Alert on `max(mcp_user_image_lifecycle_rule_present) + max(mcp_user_image_reconciler_enabled) == 0` for 15 minutes (nothing cleans up image objects), on `increase(mcp_user_image_lifecycle_reinstalls_total[1d]) > 0`, on a growing `mcp_user_image_claims_oldest_due_age_seconds`, and on `time() - mcp_user_image_reconcile_last_success_timestamp_seconds` well above `orphan_sweep_seconds`.
- **Operator signal — lifecycle rule**: `lifecycle_mode` (default `manage`) selects who owns the bucket's expiry rule for `<prefix>/`. In `manage` mode the server installs (or updates) its rule while preserving the bucket's other lifecycle rules; if another rule would not survive the rewrite exactly (for example one with two `Transition` elements), nothing is written and an ERROR `install image bucket lifecycle rule failed; ...` names the rule to add by hand (its ID, filter prefix `<prefix>/`, expire after `object_ttl_days`). Rule IDs have the form `mcp-images-<readable prefix>-<digest of the prefix>-<days>d` and a rule is replaced only when its filter prefix is exactly `<prefix>/`, so deployments whose prefixes differ only in punctuation keep each other's rules; rules installed by earlier releases (`mcp-images-<prefix>-<days>d`) are recognized and renamed. S3 and MinIO have no conditional write for the lifecycle configuration, so two writers that read it at the same moment erase each other's rule (on MinIO, four deployments starting together kept one rule out of four). The server therefore writes only when its rule is missing, reads the configuration back about 3 s after writing and merges again when the rule is gone, and checks again 1 minute after start and then every `lifecycle_check_seconds` (default 3600; 0 keeps only those two checks), reinstalling a rule another writer erased: a WARN, counted in `mcp_user_image_lifecycle_reinstalls_total`, and an ERROR `... keeps being erased ...` when the rule was gone at 3 checks in a row, which means another writer owns the whole configuration. In `verify` mode the server never writes the bucket lifecycle: manage the rule elsewhere (IaC, `mc ilm`), and each check only verifies that some enabled rule whose filter is `<prefix>/` or a parent prefix, without tag or size conditions, expires objects within `object_ttl_days`; a missing rule is an ERROR. A failed check and a missing rule are each logged at ERROR once until the rule is back (then INFO); startup always continues. While the rule is missing and the reconciler is disabled (or `object_ttl_days` is 0 with the reconciler disabled), the server also logs `image objects have no automatic cleanup` at ERROR. Prefer one owner per bucket lifecycle — a single deployment in `manage` mode, or IaC with every deployment in `verify` mode.
- **Rollout**: releases without image claims (before PR #94) upload before committing and do not record claims. While old and new instances serve the same database, a new instance's cleanup can delete an object an old instance uploaded but has not committed yet (same user and image at the same moment). Deploy the image subsystem stop-then-start, without overlapping old and new instances. Earlier releases also replace the bucket's whole lifecycle configuration on start; a new instance reinstalls its rule at its next lifecycle check.
- **Object store permissions** for the configured MinIO user on the bucket: `s3:PutObject` (uploads; without it every image request fails with `storage_unavailable`), `s3:GetObject` (presigned URLs and inline images; without it agents cannot load images), `s3:DeleteObject` (cleanup of failed requests and the reconciler; without it deletes fail with ERROR logs and objects stay until the lifecycle rule expires them), `s3:ListBucket` (the startup bucket probe, a HEAD bucket), `s3:GetBucketLocation` (region lookup), and `s3:GetLifecycleConfiguration` plus `s3:PutLifecycleConfiguration` (checking and installing the expiry rule; `verify` mode needs only the first; without them the lifecycle ERROR is logged and only the reconciler cleans up).
- **One database per bucket prefix**: object keys are content-addressed per user (`<prefix>/<user>/<sha256>.png`), so two deployments with different databases sharing a bucket and prefix (for example production and a test instance) can write the same key, and one's cleanup or GC can delete an object the other still serves. Give every deployment its own bucket or `minio.prefix`.
- **Retrieval**: agents receive presigned MinIO URLs (30-minute validity) and, when the image fits the inline budget (40 KiB per image, 80 KiB per response), also the base64 bytes directly. Claude Code consumes the inline image natively; Codex CLI calls `view_image` on the `resource_link` URI.
- **Quota probe**: `GET /api/quota` returns `{used_bytes, reserved_bytes, available_bytes, quota_bytes, object_count, ttl_days}` for the authenticated caller. `used_bytes` counts committed images; `reserved_bytes` counts images the caller's requests are still uploading (each image once, and only while no committed copy exists; the reservation of a request that crashed lapses within 10 minutes); `available_bytes` is `quota_bytes - used_bytes - reserved_bytes` (at least 0) and uses the same accounting as admission, so a new image larger than it is refused with `quota_exceeded` (an image the caller already holds or is uploading is not charged again). The report reads both counts from one snapshot. `quota_bytes` and `ttl_days` report the values in force (the defaults when the settings are unset).
- **Error codes** (response body `{"error": code, "message": ..., "attachment_index": N}`; for 5xx codes `message` is a fixed text per code and the error detail is only in the server log, while 4xx messages describe the rejected input; `internal_error` (500) is a server fault such as a failure to spool the body to a temporary file, and a malformed or aborted multipart body is `decode_failed`): `unsupported_mime` (400), `decode_failed` (422), `image_too_large` / `quota_exceeded` / `too_many_images` (413), `feature_disabled` (415), `url_blocked` (400), `url_timeout` (504), `url_fetch_failed` (502), `storage_unavailable` (503), `image_busy` (503 with `Retry-After: 5`; nothing was stored or charged, so the client may retry the same request after `Retry-After` seconds, backing off with jitter on repeated `image_busy`).
- **Per-client caveats**:
  - Claude Code renders inline `ImageContent` natively; mixed text + image messages are supported but a known Claude Code bug ([anthropics/claude-code#14150](https://github.com/anthropics/claude-code/issues/14150)) sometimes spools the result to disk.
  - Codex CLI does not render inline `ImageContent`; it relies on the `view_image` tool to fetch the `resource_link` URI introduced in v0.115.0 (2026-03-16).

### Data Storage Notes

- Requests are stored in the `mcp` PostgreSQL database, table inferred from the GORM model `askuser.Request`.
- User-authored directives powering `get_user_request` live in the `mcp_user_requests` table (model `userrequests.Request`) and are scoped by the hashed API key plus optional `task_id`. A periodic sweeper removes rows older than the configured retention window (default 30 days).
- Image attachments are metadata-only in Postgres (`mcp_user_image_refs`, with `mcp_user_request_image_links` for many-to-many binding to requests); the PNG bytes live in MinIO. See `docs/arch/mcp_pipe.md` for the image pipeline architecture.
- Primary key: UUID generated on insert.
- Sensitive fields:
  - `api_key_hash` holds the SHA-256 hash of the bearer token.
  - `key_suffix` keeps the last four characters to aid operators in identifying tokens.

## Client Integration Tips

- Use the official MCP Inspector (`/mcp/debug`) during development. Override the endpoint and token via query parameters if needed (`?endpoint=https://YOUR_HOST/mcp&token=Bearer%20...`).
- When embedding in AI agents, ensure tool calls include the `Authorization` header. For streaming MCP clients, set the header during transport initialisation.
- Handle tool errors gracefully. The server returns JSON-RPC errors with descriptive messages; agents should surface them to operators where possible.
- Consider providing AI/user identities explicitly (e.g. `user@example.com:assistant@example.com`) so that the ask_user dashboard can display meaningful attribution.

## Troubleshooting

| Symptom                                | Possible Cause                                                                                  | Remedy                                                                                                              |
| -------------------------------------- | ----------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------- |
| `ask_user tool is not available`       | PostgreSQL connection could not be opened, or configuration missing.                            | Verify `settings.db.mcp.*` values and database connectivity.                                                        |
| `web search is not configured`         | No web search engines enabled, or required credentials missing.                                 | Configure an engine under `settings.websearch.engines` (e.g. supply Google `api_key`/`cx` and set `enabled: true`). |
| Repeated `billing check failed` errors | External OneAPI billing request rejected.                                                       | Confirm the bearer token has sufficient quota or check OneAPI service health.                                       |
| `401` with `error="invalid_token"`     | one-api does not accept the key: unknown, revoked, expired, disabled, quota-exhausted or subnet-restricted. | Use an active one-api key with remaining quota; see [Credential Validation](#credential-validation).               |
| `503` with `Retry-After`               | one-api could not validate an uncached key (outage, timeout, rate limit), or more keys are active than the gate's budgets sustain. | Retry after the indicated delay; check one-api health and `settings.mcp.auth.validation.*` rate settings ([capacity](#capacity-and-scaling)). |
| `413 request_too_large`                | The request body exceeds the route budget.                                                      | Reduce the payload or raise `settings.web.request_body.*`.                                                          |
| Console shows no pending questions     | The AI has not called `ask_user`, or the API key entered does not match the one used by the AI. | Ensure matching API key and review server logs for request creation.                                                |

For additional diagnostics, enable debug logging or monitor the `/mcp` request logs emitted by the `mcp` logger namespace.
