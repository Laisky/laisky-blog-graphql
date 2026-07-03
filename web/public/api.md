# Laisky MCP Protected API

The protected API probe endpoint is [https://mcp.laisky.com/api](https://mcp.laisky.com/api).

Agents should treat `/api`, `/api/v1`, `/v1`, `/v2`, and `/agent/auth` as protected machine endpoints. When the backend route is active, those endpoints return a JSON authentication challenge and advertise the OAuth protected-resource metadata through `WWW-Authenticate`.

Use these companion resources first:

- [OpenAPI service description](https://mcp.laisky.com/openapi.json)
- [Authentication guide](https://mcp.laisky.com/auth.md)
- [OAuth protected-resource metadata](https://mcp.laisky.com/.well-known/oauth-protected-resource)
- [OAuth authorization-server metadata](https://mcp.laisky.com/.well-known/oauth-authorization-server)
- [MCP endpoint](https://mcp.laisky.com)
