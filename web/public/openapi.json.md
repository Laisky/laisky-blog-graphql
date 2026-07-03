# Laisky MCP OpenAPI

The machine-readable OpenAPI description is available at [https://mcp.laisky.com/openapi.json](https://mcp.laisky.com/openapi.json).

Use this document when an agent needs a markdown twin for the OpenAPI service description. The JSON file describes the Laisky MCP Streamable HTTP endpoint, public discovery documents, authentication metadata, status endpoint, and the related GraphQL entry point.

Important companion resources:

- [Agent navigation guide](https://mcp.laisky.com/llms.txt)
- [Full agent guide](https://mcp.laisky.com/llms-full.txt)
- [Authentication guide](https://mcp.laisky.com/auth.md)
- [RFC 9727 API catalog](https://mcp.laisky.com/.well-known/api-catalog)
- [MCP discovery endpoint](https://mcp.laisky.com/.well-known/mcp)
- [MCP server card](https://mcp.laisky.com/.well-known/mcp/server-card.json)

Agents should call the MCP endpoint with `Authorization: Bearer <api key>`. Do not place API keys in URLs or shared logs.
