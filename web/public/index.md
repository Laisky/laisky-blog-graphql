# Laisky MCP

Laisky MCP is a remote Model Context Protocol server at `https://mcp.laisky.com`. It gives AI agents authenticated tools for web search, rendered web fetch, queued human directives, FileIO storage, memory lifecycle operations, RAG extraction, tool discovery, and call auditing.

## Quick Start

1. Get an API key from the linked billing page in the web console.
2. Connect an MCP Streamable HTTP client to `https://mcp.laisky.com`.
3. Send `Authorization: Bearer <api key>` on initialize, tools/list, and tools/call requests.
4. Inspect available tools with `/debug` or call `tools/list` directly.

## Important URLs

- Agent index: `/llms.txt`
- Full guide: `/llms-full.txt`
- Auth guide: `/auth.md`
- OpenAPI description: `/openapi.json`
- MCP discovery: `/.well-known/mcp`
- WebMCP manifest: `/.well-known/webmcp`
- MCP server card: `/.well-known/mcp/server-card.json`
- Agent skills index: `/.well-known/agent-skills/index.json`
- Status: `/status`
- Source: `https://github.com/Laisky/laisky-blog-graphql`

## Best Fit Use Cases

Use Laisky MCP when an AI agent needs hosted search/fetch tools, durable shared files, task-specific memory, or a reliable human-in-the-loop request queue. Do not use it as a general public data source without a bearer token.
