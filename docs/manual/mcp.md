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

## Authentication Model

### Bearer Tokens

Both tools rely on a Bearer token supplied via the `Authorization` header. The backend accepts two formats:

1. `Authorization: Bearer <token>` — assigns default identities.
2. `Authorization: Bearer <identity>@<token>` — binds identities explicitly.

Within the identity segment you can optionally separate user and AI identities with a colon (`user-id:ai-id`). When the colon is omitted the same value is used for both. Examples:

- `Authorization: Bearer user:assistant@sk-example` → `user-id=user`, `ai-id=assistant`.
- `Authorization: Bearer workspace@sk-example` → `user-id=workspace`, `ai-id=workspace`.

The raw token is validated with one-api (see below), hashed with SHA-256 to derive the tenant namespace, and the last four characters are kept for display in the ask_user dashboard. Store tokens securely; the server does not persist them in plain text.

### Credential Validation

Every entrypoint that derives a tenant from a bearer — the MCP transport, the `/tools/*/api` console APIs, the ask_user console and the GraphQL fields shared with MCP tools — authenticates it through one authenticator (`internal/mcp/auth`). A tenant exists only for a key that one-api accepted; a key that is merely non-empty never becomes a tenant.

- **Authority.** The key is sent to one-api's read-only `GET /api/token/balance` (behind one-api `TokenAuth`, no billing side effect).
  - A key is accepted only on `200` with `Content-Type: application/json` and a bounded body carrying `"success": true`.
  - `401` and `403` reject the key.
  - Everything else means one-api gave no verdict: `429`, `5xx`, redirects, transport errors, timeouts, and also `200 {"success": false}` or an HTML page.
- **MCP transport responses.** A presented but rejected key receives `401` with `WWW-Authenticate: Bearer error="invalid_token", error_description="…", resource_metadata="https://mcp.laisky.com/.well-known/oauth-protected-resource"` before any session is issued or any tool runs. If no verdict can be obtained, the response is `503` with `Retry-After: 5`, never `401` and never a tenant.
- **Console and GraphQL responses.** Console APIs keep their `{"error": "..."}` JSON with `401`, or `503` when no verdict is available. GraphQL keeps its normal error path.
- **Anonymous discovery.** Requests without any credential keep working for `initialize`, `tools/list` and similar discovery calls. This includes an `Authorization: Bearer` header with an empty token. Tools still require a valid key.
- **One validation per request.** The verified identity travels in the request context, so nested `mcp_pipe` steps and every handler reuse it.
- **Caching.**
  - An accepted key is trusted for `positive_ttl` (default 5 minutes); this is also the revocation lag.
  - A rejected key is refused for `negative_ttl` (default 30 seconds) without asking one-api again.
  - Concurrent lookups of the same key share one upstream call.
  - A key stays *known* for 24 hours after its last acceptance; this decides whether its revalidation may use the reserved budget below.

#### One-api upstream gate

Every call from this server to one-api's per-IP-limited REST API goes through one gate (`mcpauth.UpstreamGate`). That covers API key validation and revalidation, the `POST /api/oneapi/balance` proxy, and the GraphQL `ValidateOneapiAPIKey` query.

- A call is charged only when it is about to start. Cache hits, negative-cache hits and coalesced waiters are free.
- Balance answers are reused per key hash for 30 seconds. A balance call also records the key's validation verdict, so `ValidateOneapiAPIKey` and the MCP entrypoints share one cache.

The gate's budgets:

- **Per-source budget.** Each client source may start `per_source_burst` new-key or balance calls (default 5), refilled at `per_source_rate_per_second` (default 0.1, i.e. one per 10 seconds).
  - A source is an IPv4 address or an IPv6 `/64`, using `clientip.NetworkKey`.
  - Sources are tracked in an LRU bounded by `max_sources` (default 10,000).
  - A source over its budget gets `503` with `Retry-After` without spending the global budget.
- **Global budget.** All sources together are limited to `upstream_rate_per_second` (default 1) with `upstream_burst` (default 30), and to `max_concurrent` (default 8) calls in flight. This protects one-api's per-IP limit.
- **Reserved revalidation budget.** A known key whose trust period ended revalidates from a reserve that unknown-key and balance traffic cannot spend: `revalidation_rate_per_second` (default 0.2), with `revalidation_burst` (default 20).
  - It falls back to the per-source and global budgets when the reserve is empty.
  - Each key needs at most one revalidation per `positive_ttl`, so the default supports about 60 actively used keys per 5 minutes.
  - `0` disables the reserve.
- **Client source.** The source is the identity the request-identity middleware resolves with the trusted-proxy policy (`library/clientip`, see `docs/manual/client_identity.md`). Forwarding headers count only when sent by a configured trusted proxy, so one direct peer that rotates `X-Forwarded-For` stays one source. Handlers served outside gin fall back to the TCP peer.

Outages and local refusals behave differently:

- **Genuine one-api failures.** A timeout, transport error, `429`, `5xx` or a non-success `200` lets a key that one-api accepted within `positive_ttl + stale_grace` (default grace 30 minutes) stay usable (stale-if-error). Any other key fails closed with `503`.
- **Retry spacing.** After such a failure, one key is retried upstream at most every 15 seconds, so an outage cannot spend the reserve on one key.
- **Local refusals.** A refusal by this server's own budgets never grants stale grace. Emptying a budget therefore cannot keep a revoked key alive past `positive_ttl`.

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
- **Rate limits.** one-api's per-IP REST rate limit (`GLOBAL_API_RATE_LIMIT`, default 480 per 3 minutes) covers every call from this server.
  - With the defaults, the gate uses at most `1/s × 180 + 30 = 210` new-key and balance calls plus `0.2/s × 180 + 20 = 56` revalidations per 3 minutes. That leaves about 214 per 3 minutes for paid-tool `/api/token/consume` calls.
  - Consume calls are tied to verified keys and are never refused by the gate. If paid-tool traffic exceeds that remainder, one-api answers them with `429`. The billing outcome is then classified as denied (no quota spent) and the paid tool fails. Lower the gate's rates if consume traffic needs more of the limit.

Settings (`settings.mcp.auth.validation.*`). For `stale_grace` and `revalidation_rate_per_second`, an explicit `0` disables the feature; an unset key keeps the default. Other zero or negative values fall back to the default or the minimum shown.

| Key                        | Default                    | Meaning                                                      |
| -------------------------- | -------------------------- | ------------------------------------------------------------ |
| `enabled`                  | `true`                     | One-api validation. `false` is a development-only opt-out that logs a WARN at startup and accepts any non-empty bearer. |
| `base_url`                 | `settings.oneapi.balance_url` origin, else `oneapi.BillingAPI` | one-api origin.             |
| `timeout`                  | `5s` (100ms–30s)           | Upstream timeout per call, including the wait for a slot.    |
| `positive_ttl`             | `5m` (1s–1h)               | Trust period for an accepted key (revocation lag).           |
| `negative_ttl`             | `30s` (1s–5m)              | Refusal period for a rejected key.                           |
| `stale_grace`              | `30m` (0–24h, `0` disables) | Extra period during which an accepted key survives a genuine one-api failure. |
| `max_entries`              | `10000`                    | Cardinality bound of each key cache.                         |
| `max_concurrent`           | `8`                        | Concurrent upstream calls.                                   |
| `upstream_rate_per_second` | `1`                        | Global rate of new-key and balance calls.                    |
| `upstream_burst`           | `30`                       | Global burst of new-key and balance calls.                   |
| `per_source_rate_per_second` | `0.1`                    | Refill rate of each client source's budget.                  |
| `per_source_burst`         | `5`                        | Calls one client source (IPv4 address or IPv6 /64) may start at once. |
| `max_sources`              | `10000`                    | Client sources tracked (least recently used evicted).        |
| `revalidation_rate_per_second` | `0.2` (`0` disables)   | Reserved rate for revalidating known keys.                   |
| `revalidation_burst`       | `20`                       | Reserved burst for revalidating known keys.                  |

Logging: rejected bearers are logged at DEBUG. Unavailable verdicts and stale-grace answers produce at most one WARN summary per minute per entrypoint, with a suppressed-count field, plus DEBUG lines per request.

### Request Size and Time Limits

Request bodies are budgeted per route family before any middleware or handler reads them.

- **Byte cap.** A declared `Content-Length` above the cap is refused with `413` before a byte is read or a credential is validated. Bodies without a declared length are capped while streaming.
- **Minimum data rate.** Reads follow a minimum-data-rate rule, like Kestrel's `MinRequestBodyDataRate`. A client gets `read_grace`, and must then keep delivering at least `min_data_rate` bytes per second. The deadline moves forward as bytes arrive. Large uploads over slow but live links therefore succeed, while a stalled or trickling (slow-loris) body is cut.
- **Absolute cap.** The whole read is limited to the smaller of `max_read_duration` and `read_grace + cap / min_data_rate`.
- **Bodyless requests.** Requests without a body (SSE `GET` streams, held long polls after their body was read, websockets) are never limited.

Settings (`settings.web.request_body.*`, plus `settings.web.idle_timeout`):

| Key                       | Default   | Applies to                                                                                     |
| ------------------------- | --------- | ---------------------------------------------------------------------------------------------- |
| `mcp_max_bytes`           | 32 MiB    | MCP transport requests that carry a validated key. This leaves room for the largest tool call, `extract_key_info` with 10 MB of materials. |
| `mcp_anonymous_max_bytes` | 1 MiB     | MCP transport requests without credentials (discovery only).                                   |
| `graphql_max_bytes`       | 64 MiB    | `/query` and `/query/v2`. gqlgen's multipart upload limit (32 MiB) still applies inside.        |
| `default_max_bytes`       | 1 MiB     | Every other route (one-api balance proxy, SSO code exchange, webhooks). Routes with tighter own caps keep them. |
| `min_data_rate`           | `4096` B/s | Minimum delivery rate after the grace period, on every route.                                 |
| `read_grace`              | `10s`     | Time before the first bytes must arrive.                                                       |
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
- **Accepted formats**: `image/jpeg`, `image/png`, `image/webp`, `image/bmp`, `image/tiff`, and the first frame of `image/gif`. `image/svg+xml` and `image/heic` are rejected (`unsupported_mime`). Every stored object is re-encoded as PNG after decode -> orientation fix -> resize to a 1536-pixel longest edge.
- **Storage & expiry**: images live in MinIO keyed by SHA256 under the configured prefix. A bucket-level lifecycle rule expires objects after 7 days; the same SHA from the same user dedupes and refreshes the TTL. A DB-side GC hourly reaps expired metadata rows.
- **Retrieval**: agents receive presigned MinIO URLs (30-minute validity) and, when the image fits the inline budget (40 KiB per image, 80 KiB per response), also the base64 bytes directly. Claude Code consumes the inline image natively; Codex CLI calls `view_image` on the `resource_link` URI.
- **Quota probe**: `GET /api/quota` returns `{used_bytes, quota_bytes, object_count, ttl_days}` for the authenticated caller.
- **Error codes** (response body `{"error": code, "message": ..., "attachment_index": N}`): `unsupported_mime` (400), `decode_failed` (422), `image_too_large` / `quota_exceeded` / `too_many_images` (413), `feature_disabled` (415), `url_blocked` (400), `url_timeout` (504), `url_fetch_failed` (502), `storage_unavailable` (503).
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
| `503` with `Retry-After`               | one-api could not validate an uncached key (outage, timeout, rate limit).                       | Retry after the indicated delay; check one-api health and `settings.mcp.auth.validation.*` rate settings.          |
| `413 request_too_large`                | The request body exceeds the route budget.                                                      | Reduce the payload or raise `settings.web.request_body.*`.                                                          |
| Console shows no pending questions     | The AI has not called `ask_user`, or the API key entered does not match the one used by the AI. | Ensure matching API key and review server logs for request creation.                                                |

For additional diagnostics, enable debug logging or monitor the `/mcp` request logs emitted by the `mcp` logger namespace.
