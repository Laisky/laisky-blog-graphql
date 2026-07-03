# Laisky MCP Remote Tools

Use this skill when an AI agent needs the remote Laisky MCP server at `https://mcp.laisky.com`.

## Capabilities

- Search the public web with `web_search`.
- Render dynamic web pages with `web_fetch`.
- Receive queued human directives with `get_user_request`.
- Read, write, list, search, delete, and rename project-scoped files with FileIO.
- Prepare and persist task memory with memory lifecycle tools.
- Extract key passages from supplied materials with `extract_key_info`.

## Authentication

Send `Authorization: Bearer <api key>` on every MCP request. Keep the key in secret storage and do not write it into FileIO, memory, logs, or chat transcripts.

## Repository Defaults

For this repository, use `task_id=graphql` and `project=graphql`.
