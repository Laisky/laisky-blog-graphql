# Laisky MCP Pricing

Laisky MCP includes free console and coordination tools plus paid metered tools where external providers charge per call.

## Free Tools

- MCP Inspector
- `get_user_request`
- FileIO tools
- memory lifecycle tools
- call log viewer
- `extract_key_info` when enabled without additional provider cost

## Metered Tools

- `web_search`: displayed in the console as `$0.005/call`.
- `web_fetch`: displayed in the console as `$0.0001/call`.

The exact billing decision is enforced by the bearer token's external billing account. Agents should surface billing errors to the human operator and avoid repeated identical calls when cached context is available.
