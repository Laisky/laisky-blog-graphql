# MCP Server Manual

## Overview

- Implements an HTTP transport for the Model Context Protocol (MCP) at <https://mcp.laisky.com>.
- Exposes optional tools: `web_search`, `web_fetch`, and `ask_user`; each tool is added only when its dependencies are configured.
- Purchase API_KEY at <https://wiki.laisky.com/projects/gpt/pay/#page_gpt_pay>.

## Architecture

- `internal/mcp/server.go` builds the MCP server, wires hooks, and wraps the streamable HTTP handler with request/response logging capped at 4 KB per body.
- Authorization is propagated through the request context so tools can extract a Bearer token via `Authorization` headers.
- Each tool implementation resides in `internal/mcp/tools`. Tools return structured JSON payloads using `mcp.NewToolResultJSON` and map recoverable errors to MCP tool errors.
- When a `callRecorder` is provided, every tool invocation is persisted through `calllog.Service`, storing timing, status, parameters, and billing cost.
- Auxiliary HTTP services:
    - `internal/mcp/askuser/http.go` serves a lightweight dashboard for human responses.
    - `internal/mcp/calllog/http.go` exposes paginated call logs filtered by the caller’s API key hash.

## Tool Guide

### web_search

- **Purpose:** Runs Google Programmable Search (or other configured engines) via `searchlib.Provider`.
- **Dependencies:** `searchProvider`, Redis (optional), billing checker, and an API key in the request context.
- **Input:** `{ "query": "plain text" }`.
- **Output:** JSON-encoded `library/search.SearchResult` with `query`, `created_at`, and `results` array (URL, title, snippet).
- **Billing & Logging:** Charges `oneapi.PriceWebSearch`; successes record cost, failures record zero.
- **Usage Steps:**
    1.  Call `mcp/initialize` to obtain tool manifest.
    2.  Issue `call_tool` with `"tool_name": "web_search"` and the JSON arguments shown above.
    3.  Ensure the Authorization header contains `Bearer <api key>`; missing tokens return `"missing authorization bearer token"`.

### web_fetch

- **Purpose:** Fetches fully rendered HTML (supports dynamic content) using `searchlib.FetchDynamicURLContent` and Redis-backed caching.
- **Dependencies:** Redis connection, API key provider, billing checker.
- **Input:** `{ "url": "https://example.com" }`.
- **Output:** JSON with `url`, `content` (HTML as a string), and `fetched_at` timestamp (UTC).
- **Billing & Logging:** Deducts `oneapi.PriceWebFetch` on success; errors log without cost.
- **Usage Steps:**
    1.  Authenticate with a Bearer token.
    2.  Send `call_tool` for `web_fetch` with the target URL.
    3.  Handle tool errors such as billing denial (`billing check failed: ...`) or upstream fetch failures.

### ask_user

- **Purpose:** Allows an AI agent to pose a question to a human operator and wait for a reply.
- **Dependencies:** `askuser.Service`, Authorization header parser, optional dashboard assets.
- **Input:** `{ "question": "What should I do next?" }`.
- **Flow:**
    1.  Tool validates and trims the question.
    2.  Authorization header is parsed into `AuthorizationContext` (API key hash, user identity prefix).
    3.  Stores the question via `Service.CreateRequest` and blocks until `WaitForAnswer` returns or timeout (`default 5 min`).
    4.  On timeout, the request is marked `expired`; otherwise the supplied answer is returned.
- **Output:** JSON containing `request_id`, `question`, `answer`, `asked_at`, and optionally `answered_at`.
- **Usage Steps:**
    1.  Provide a Bearer token tied to the human operator; malformed headers yield `"invalid authorization header"`.
    2.  Invoke `call_tool` with `"tool_name": "ask_user"`.
    3.  Ensure an operator responds through the Ask User console (see below) before the timeout elapses.

## Supporting Services

- **Ask User Console:** Served at `<prefix>/tools/ask_user`. Allows operators to monitor pending questions and submit answers via the stored API key. Static assets are auto-discovered; falls back to an embedded template when build artifacts are absent.
- **Call Log API:** Available at `<prefix>/tools/call_log/api/logs`. Requires the same Authorization header; returns paginated entries filtered to the caller’s API key hash. Supports query parameters `page`, `page_size`, `tool`, `user`, `from`, `to`, `sort_by`, and `sort_order` (date ranges treat `to` as exclusive of the next day).
- **Status Endpoint:** `<prefix>/status` responds with `200` for GET/HEAD/OPTIONS to verify MCP availability.

## Running Locally

- Ensure configuration (`settings.*`) supplies:
    - Redis connection for `web_fetch` caching.
    - Web search engine credentials (e.g., Google API key + CX).
    - PostgreSQL DSN for Ask User and Call Log services if those tools are needed.
- Launch the API service: `go run ./cmd api`. The MCP server auto-initializes when at least one tool dependency is satisfied.
- Visit `http://localhost:<port>/mcp/status` to confirm the MCP handler is active.

## Example MCP Client Workflow

- **1. Discover Tools**
    - POST to `/mcp/` (or configured prefix) with the MCP `initialize` request; inspect the returned tool definitions and instructions string: “Use web_search for Google Programmable Search queries and web_fetch to retrieve dynamic web pages.”
- **2. Invoke Tools**
    - Always include `Authorization: Bearer <api key>`.
    - Submit `call_tool` payloads such as:
        ```json
        {
            "jsonrpc": "2.0",
            "id": 1,
            "method": "call_tool",
            "params": {
                "tool_name": "web_search",
                "arguments": { "query": "golang async streams" }
            }
        }
        ```
- **3. Process Responses**
    - On success, parse `result.content[0]` (text JSON) or `result.structured_content`.
    - On failure, surface `result.is_error` messages to the operator; common causes are billing failures, missing authorization, or invalid arguments.
- **4. Review Logs**
    - (Optional) Query the Call Log API to audit usage or reconcile billing.

## Troubleshooting

- **No tools returned:** Check that at least one dependency was provided when constructing `mcp.NewServer`; review startup logs for “skip mcp server initialization.”
- **Authorization errors:** Ensure tokens are not blank after stripping the Bearer prefix; the console displays the linked user identity prefix for quick validation.
- **Billing denials:** Quotas are enforced via `oneapi.CheckUserExternalBilling`; inspect external billing service status and the Call Log API error messages.
- **Ask User timeouts:** Verify operators are logged into the console and that PostgreSQL is reachable; the tool marks requests expired if no answer arrives before the configured timeout.
