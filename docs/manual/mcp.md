# Remote MCP Server Manual

This guide explains how to connect to the remote Model Context Protocol (MCP) endpoint exposed by the GraphQL service, the tools that are available, the HTTP helper pages that accompany them, and the configuration that must be in place before use.

## Overview

The service mounts an MCP-compatible JSON-RPC endpoint at `/mcp`. Clients may connect over HTTP(S) using any MCP client, such as the [MCP Inspector](https://modelcontextprotocol.io/), and interact with two tools:

- `web_search` — runs a Bing-powered web search.
- `ask_user` — forwards a question to the authenticated human and waits for their reply.

Both tools require a valid `Authorization: Bearer <token>` header. Tokens are also used for billing and for routing questions to the correct user.

## Deployment Prerequisites

Enable the MCP endpoint when starting the API service. The tools are advertised automatically when their dependencies are available.

| Feature      | Requirement                                                                                                                                      |
| ------------ | ------------------------------------------------------------------------------------------------------------------------------------------------ |
| `web_search` | `settings.websearch.bing.api_key` must be configured. Billing is performed against the token owner via `oneapi.CheckUserExternalBilling`.        |
| `ask_user`   | PostgreSQL connection info under `settings.db.mcp` (`addr`, `db`, `user`, `pwd`). The service runs database migrations automatically using GORM. |

If no tool dependencies are met the server skips MCP initialisation.

## Authentication Model

### Bearer Tokens

Both tools rely on a Bearer token supplied via the `Authorization` header. The backend accepts two formats:

1. `Authorization: Bearer <token>` — assigns default identities.
2. `Authorization: Bearer <identity>@<token>` — binds identities explicitly.

Within the identity segment you can optionally separate user and AI identities with a colon (`user-id:ai-id`). When the colon is omitted the same value is used for both. Examples:

- `Authorization: Bearer user:assistant@sk-example` → `user-id=user`, `ai-id=assistant`.
- `Authorization: Bearer workspace@sk-example` → `user-id=workspace`, `ai-id=workspace`.

The raw token is checked against the external billing service, hashed with SHA-256, and the last four characters are kept for display in the ask_user dashboard. Store tokens securely; the server does not persist them in plain text.

## HTTP Endpoints

| Path                                    | Methods           | Purpose                                                                                                       |
| --------------------------------------- | ----------------- | ------------------------------------------------------------------------------------------------------------- |
| `/mcp`                                  | `POST`, WebSocket | Primary MCP transport, handled by the streamable MCP server.                                                  |
| `/mcp/debug`                            | `GET`, `HEAD`     | Serves a pre-configured MCP Inspector page. Optional `?endpoint=` and `?token=` query params allow overrides. |
| `/mcp/tools/ask_user`                   | `GET`             | Web console for human operators. Prompts for the API key and then shows pending questions and history.        |
| `/mcp/tools/ask_user/api/requests`      | `GET`             | JSON API used by the console. Requires the same `Authorization` header.                                       |
| `/mcp/tools/ask_user/api/requests/{id}` | `POST`            | Submits the human response for a pending request.                                                             |

> **Note:** The console endpoints are intended for browsers. They are protected only by the bearer token, so deploy behind HTTPS and avoid exposing them publicly without additional access controls.

## Tool Reference

### `web_search`

- **Description:** Search the public web using Bing and return structured results.
- **Input Parameters:**
  - `query` (string, required) — plain text search phrase.
- **Behaviour:**
  1. Validates the token and charges the user via `oneapi.CheckUserExternalBilling` (`PriceWebSearch`).
  2. Issues the request to the configured Bing search engine.
  3. Returns a JSON payload compatible with `search.SearchResult`.
- **Sample Result:**

```json
{
  "query": "mcp protocol overview",
  "created_at": "2025-10-23T18:24:26Z",
  "results": [
    { "url": "https://example.com", "name": "Example", "snippet": "..." }
  ]
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

### Data Storage Notes

- Requests are stored in the `mcp` PostgreSQL database, table inferred from the GORM model `askuser.Request`.
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

| Symptom                                | Possible Cause                                                                                  | Remedy                                                                        |
| -------------------------------------- | ----------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------- |
| `ask_user tool is not available`       | PostgreSQL connection could not be opened, or configuration missing.                            | Verify `settings.db.mcp.*` values and database connectivity.                  |
| `web search is not configured`         | Bing API key missing.                                                                           | Set `settings.websearch.bing.api_key`.                                        |
| Repeated `billing check failed` errors | External OneAPI billing request rejected.                                                       | Confirm the bearer token has sufficient quota or check OneAPI service health. |
| Console shows no pending questions     | The AI has not called `ask_user`, or the API key entered does not match the one used by the AI. | Ensure matching API key and review server logs for request creation.          |

For additional diagnostics, enable debug logging or monitor the `/mcp` request logs emitted by the `mcp` logger namespace.
