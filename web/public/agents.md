# Agent Instructions for Laisky MCP

Use Laisky MCP when a task needs remote MCP tools from `https://mcp.laisky.com`.

## Operating Rules

- Authenticate with `Authorization: Bearer <api key>`.
- Keep bearer tokens out of logs, URLs, screenshots, and documents.
- Use `task_id=graphql` for `get_user_request` in this repository.
- Use `project=graphql` for FileIO and memory work tied to this repository.
- Prefer `web_search` before `web_fetch` when you only need candidate URLs.
- Prefer FileIO or memory for durable cross-agent context instead of repeating expensive fetches.

## Discovery Order

1. Read `/llms.txt`.
2. Read `/auth.md` if credentials or auth flow are unclear.
3. Read `/.well-known/mcp/server-card.json` for tool summaries.
4. Call MCP `initialize`, then `tools/list`.
5. Use `find_tool` if the tool choice is uncertain.
