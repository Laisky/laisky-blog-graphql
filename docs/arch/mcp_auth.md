# MCP Credential Authenticator Architecture

Last updated: 2026-11-16 (UTC)

This manual describes how every entrypoint of this server turns a presented API key into a tenant, and how the server's calls to one-api are budgeted. The product decision behind it (which keys are tenants, what an exhausted key may reach) is `docs/requirements/mcp_credential_policy.md`. Operator settings, capacity figures and alerts are in `docs/manual/mcp.md` (Credential Validation). Research notes and sources are in `docs/ref/20261108_mcp_ingress_auth_body_budget.md`.

## 1. Invariants

Each invariant has a test that fails when it breaks. `TestIngressInventory` (`internal/mcp/auth/ingress_inventory_test.go`) scans every non-test Go file of the module, so a new reader of credentials, a new one-api REST call or a new way to build an identity cannot be added without being registered here.

| # | Invariant | Enforced by |
| --- | --- | --- |
| I1 | Only `Authenticator.Authenticate` produces a verified `mcpauth.Context`; `mcpauth.FromContext` ignores every other value, and only `internal/mcp/auth` writes `ctxkeys.AuthContext`. | `TestIngressInventory` (writers), `TestFromContextIgnoresUnverifiedContexts` |
| I2 | Every inbound reader of a credential (an `Authorization` header, the MCP query key, a key typed into Telegram, a key argument) is registered below with the call that authenticates it. | `TestIngressInventory` (readers) |
| I3 | Every one-api REST call (`/api/...`, limited by one-api per client address) is either admitted by the `UpstreamGate` (validation, revalidation, balance) or recorded in `oneapi.CallLedger` (consume), so the gate sees the combined traffic. | `TestIngressInventory` (routes), `TestGateYieldsToConsumesInOneAPIWindow` |
| I4 | A key longer than `MaxAPIKeyLength` (256 bytes) or an `Authorization` header longer than 2 KiB is refused before it is hashed, looked up or sent anywhere. | `TestOversizedKeyIsRefusedBeforeDerivation` |
| I5 | A key accepted before is served past its trust period only while one-api itself gave no verdict. A refusal by this server's budgets and a waiter whose own request ended never extend a key's life. | `TestCanceledWaiterNeverReceivesStaleGrace`, `TestLocalBudgetRefusalNeverGrantsStaleGrace` |
| I6 | A key (the caller's or the platform's) is only sent to the configured origin: no client that carries one follows a redirect. | `TestOneAPIValidatorRefusesRedirectsWithInjectedClient`, `TestCheckUserExternalBillingNeverFollowsRedirects`, `TestOpenAIEmbedderRefusesRedirects`, `TestCohereRerankClientRefusesRedirects`, `TestResponsesHelperRefusesRedirects`, `TestOpenAILLMRefusesRedirects` |
| I7 | The tenant is `sha256(TrimSpace(key))`, unchanged since before validation existed, so stored data keeps its owner. | `TestTenantHashIsSHA256OfTheTrimmedKey`, `TestValidateOneAPITokenUsesSharedAuthenticator` |
| I8 | `mcpauth.Context` is the one identity type. `askuser.AuthorizationContext` is an alias of it, `askuser.FromSharedContext` returns only verified contexts, `files.AuthFromVerified` is the only conversion to the files view, and `askuser.NewTelegramAnswerer` is the one, explicit, key-less delegated principal. | `TestIngressInventory` (literals), `TestFileAuthFromContextRefusesUnverifiedIdentity`, `TestFromSharedContextRefusesUnverified` |

## 2. Components

```
            bearer / key
                 │
        ┌────────▼─────────┐  parse + bound (I4)
        │  Authenticator   │──────────────────────────► ErrInvalidAuthorization
        │  (policy)        │
        │                  │  verdictStore.trust(hash)
        │   fresh ─────────┼──────────────────────────► verified (cache)
        │   rejected ──────┼──────────────────────────► ErrCredentialRejected (cache)
        │   unknown/expired│
        │        │         │  singleflight per hash
        │        ▼         │
        │  UpstreamGate ───┼── refused (budget) ──────► ErrUpstreamThrottled (503, no stale)
        │        │ admitted│
        │        ▼         │
        │  Validator ──────┼── 200 JSON success ──────► verified; store records acceptance
        │  (one-api)       ├── 401 / 403 ─────────────► ErrCredentialRejected; store records rejection
        │                  └── anything else ─────────► stale grace (known key, I5) or 503
        └──────────────────┘
```

- **`Authenticator`** (`internal/mcp/auth/authenticator.go`) applies the policy in the order above. It is installed process-wide at startup (`Install`) and resolved per request, so every entrypoint shares one cache and one gate.
- **`verdictStore`** (`verdicts.go`) only remembers what one-api answered, per key hash: the last acceptance (trusted for `positive_ttl`, *known* for 24 hours), recent rejections (`negative_ttl`) and recent balance answers (30 s). Every update runs under one lock, so an acceptance observed from a consume cannot undo a concurrently recorded rejection.
- **`UpstreamGate`** (`upstream_gate.go`) decides whether a one-api call may start now. In order: one-api's own per-IP window (shared with consumes), the reserved revalidation budget for known keys, the caller's source budget, the global budget, then a concurrency slot. Budgets are charged only once the call holds a slot; a refusal never queues.
- **Sources.** A source is the client address the trusted-proxy policy resolved (`library/clientip`), grouped as an IPv4 address or an IPv6 `/64`; a configured gateway range (`gateways`) replaces that with one shared source and its own budget; a caller without an address names its source (`WithBudgetSource`, e.g. `telegram:<uid>`). Forwarding headers from an untrusted peer never select a source.
- **`OneAPITokenValidator`** (`oneapi_validator.go`) calls `GET /api/token/balance`. Only `200`, `application/json`, `"success": true` accepts; `401`/`403` reject; everything else is no verdict. It never follows redirects. The deprecated `settings.oneapi.balance_url` is used verbatim as the endpoint (`NewOneAPITokenValidatorAt`).
- **`oneapi.CallLedger`** (`library/billing/oneapi/ledger.go`) counts this process's one-api REST calls over one-api's 3-minute sliding window. Consumes record unconditionally (the tool call already ran); the gate records atomically with its admission and refuses once the window holds `oneapi_rate_limit` calls.
- **`BeginAuthorityCall`** (`authority_observation.go`) lets a consume that proves acceptance (`oneapi.ConsumeReceipt.TokenAccepted`) restart a known key's trust period, so keys that call tools need no revalidation calls.

## 3. Credential ingress inventory

Every entrypoint that reads a credential, and the call that authenticates it. `TestIngressInventory` holds the same list.

| Entrypoint | Credential | Authenticated by |
| --- | --- | --- |
| MCP transport (`internal/mcp/server_auth.go`, `server_ingress.go`) | `Authorization` header, or the `APIKEY`/`apikey`/`api_key` query key | `withBearerAuthentication` → `Authenticate`, before sessions, the SDK or any tool; anonymous requests may only discover (`server_ingress_anonymous.go`) |
| Tool consoles `/tools/{call_log,get_user_requests,file_io}/api/*` | `Authorization` | `mcpauth.HTTPMiddleware` → `Authenticate`; handlers then read the verified context (`askuser.ParseAuthorizationFromContext`) |
| ask_user console `/tools/ask_user/*` (`internal/mcp/askuser/http.go`) | `Authorization` | `askuser.ParseAuthorizationFromContext` → `FromContextOrHeader` → `Authenticate` |
| GraphQL FileIO / Memory fields (`internal/library/fileio/resolver.go`) | `Authorization` of the HTTP request or WebSocket handshake | `FromContextOrHeader`, then `files.AuthFromVerified` |
| GraphQL ExtractKeyInfo (`internal/library/rag/resolver.go`) | `Authorization` | `FromContextOrHeader` |
| GraphQL WebFetch / WebSearch (`internal/library/search/service.go`) | `Authorization` | `FromContextOrHeader` |
| GraphQL ArweaveUpload, API-key path (`internal/web/arweave/controller/arweave.go`) | `Authorization` when no blog JWT is presented | `FromContextOrHeader`, before the consume |
| GraphQL ValidateOneapiAPIKey (`internal/web/general/controller/general.go`) | `api_key` argument | `Authenticator.Balance` (no tenant) |
| `POST /api/oneapi/balance` (`internal/web/oneapi_proxy.go`) | JSON `api_key` | `Authenticator.Balance` (no tenant) |
| Telegram `/askuser` and upload key linking (`internal/web/telegram/service`) | key typed into the chat | `Authenticate` with `WithBudgetSource("telegram:<uid>")`; the binding stores the verified hash (ask_user) or key (uploads) |
| Telegram ask_user answers | Telegram sender + binding | delegated principal `askuser.NewTelegramAnswerer` (no key, never verified) |
| Blog posts (`internal/web/blog/service/posts.go`) | SSO JWT bearer | JWT signature (not an API key) |

Keys used later without a request: the file index worker reuses the caller's key from the encrypted credential store within `credential_cache_ttl_seconds`; Telegram uploads reuse the bound key. Both only send the key to one-api, whose `TokenAuth` refuses it once revoked.

## 4. one-api call inventory

one-api limits its `/api` group per client address (`GLOBAL_API_RATE_LIMIT`, default 480 per 3 minutes, sliding) and its relay routes (`/v1/...`) per token (`GLOBAL_RELAY_RATE_LIMIT`). Only the `/api` calls share a budget across all callers of this server.

| Call | Route | Budget at this server |
| --- | --- | --- |
| Validation, revalidation | `GET /api/token/balance` | `UpstreamGate` (source → global, or the revalidation reserve) + ledger |
| Balance proxy, `ValidateOneapiAPIKey` | `GET /api/token/balance` | `UpstreamGate` (source → global) + ledger |
| Tool charges and zero-cost usage reports (MCP and GraphQL), ArweaveUpload, Telegram uploads | `POST /api/token/consume` | Recorded in the ledger; never refused locally, because the work already ran |
| Embeddings, rerank, Responses (file index), memory heuristic, PageIndex LLM | `/v1/...` relay | Per token at one-api; not in the ledger |

With the defaults the gate starts at most 266 calls per window, leaving at least 214 for consumes. When consumes need more, the gate yields: it starts no call once the ledger holds `oneapi_rate_limit` calls, so validation never pushes the process over one-api's limit.

## 5. Failure modes

| Situation | Result | Signal |
| --- | --- | --- |
| one-api down, slow, `429`, `5xx`, non-success `200` | Known keys within `positive_ttl + stale_grace` served; others `503` | `mcp_auth_decisions_total{result="accepted_stale"|"unavailable_authority"}` |
| Flood of unknown keys from one source | That source gets `503` after its burst; others unaffected | `mcp_auth_gate_refusals_total{budget="per_source"}` |
| Flood from many sources | Global budget spent; known keys revalidate from the reserve | `{budget="global"}`, `unavailable_local_budget` |
| Consume traffic fills one-api's window | Gate yields (`503` for new keys, no stale grace) | `{budget="oneapi_window"}`, `oneapi_rest_calls_in_window` |
| one-api token-store failure (answers `401`) | Expired-trust keys rejected until it recovers | `mcp_auth_known_key_rejections_total` |
| Several replicas behind one egress | Budgets are per process and add up | set each replica's `oneapi_rate_limit` to its share |
