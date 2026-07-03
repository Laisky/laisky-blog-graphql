# Laisky MCP Agent Authentication

This document describes how AI agents authenticate to Laisky MCP.

## agent_auth

Laisky MCP uses bearer-token authentication for MCP Streamable HTTP. Send the token in the HTTP header:

```http
Authorization: Bearer <api key>
```

Do not put the token in query parameters. Do not log it. Do not store it in shared files.

## Discover

- Protected resource metadata: `https://mcp.laisky.com/.well-known/oauth-protected-resource`
- Authorization server metadata: `https://mcp.laisky.com/.well-known/oauth-authorization-server`
- Human key purchase and account page: `https://wiki.laisky.com/projects/gpt/pay/`
- MCP endpoint: `https://mcp.laisky.com`

## Pick a Method

Use a bearer API key for MCP tools. Some browser SSO pages support OAuth-style user login, but MCP tool calls should use the API key issued for tool access.

## Register

`register_uri`: `https://wiki.laisky.com/projects/gpt/pay/`

A human operator obtains or funds the API key, then provides it to the agent through the agent host's secret storage.

## Claim

No autonomous credential claim endpoint is exposed. If an agent needs a new key, ask the authenticated human operator to create one.

## Use the Credential

Call `POST https://mcp.laisky.com` with:

```http
Content-Type: application/json
Accept: application/json, text/event-stream
Authorization: Bearer <api key>
```

Then send MCP `initialize`, `tools/list`, and `tools/call` JSON-RPC payloads.

## Errors

- Missing token: the MCP tool or transport returns an authorization error.
- Invalid token: replace the key in the agent host secret store.
- Insufficient balance: the tool returns a billing error; ask the human to fund or rotate the key.

## Revocation

Revoke or rotate credentials from the same account page used to obtain the key. After revocation, remove the old key from agent host storage.

## Spec Keywords

This page intentionally includes the discovery keywords agents look for: `agent_auth`, `register_uri`, `identity_assertion`, `id-jag`, and `WWW-Authenticate`.
