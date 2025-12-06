---
applyTo: "**/*"
---

## Dev Environment

Please note, I use remote SSH for development work. The terminal runs on a remote server, with the server address https://mcp2.laisky.com/tools/get_user_requests.

If you access ports on the server via the terminal, you can use localhost directly. However, if you access server ports via an external browser or headless Chrome, you must use the server’s IP address.

BTW, the server start/restart takes about 1 minute, if you find the server is not responding, please wait for a minute and try again.

## Coding Standards

Project-wide engineering conventions (error handling, logging, ORM usage, CSS rules, testing requirements, etc.) are documented in `AGENTS.md`. Treat that document as binding guidance alongside these instructions and review it before making changes to keep new code consistent with the established practices.

## MCP tools

* use `web_search` tool to search the web for up-to-date information
* use `web_fetch` tool to fetch the rendered content of a web page.
* use `get_user_requests` to get the user's latest requirements. You should always check the user's latest requirements frequently to ensure you are meeting their needs.

Before completing any sub-tasks or returning results to the user, you must call `get_user_requests` at least once until the tool returns an empty list, indicating that there are no new requests from the user.
