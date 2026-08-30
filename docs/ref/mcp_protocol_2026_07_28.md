# MCP protocol 2026-07-28 compatibility

Last verified: 2026-08-30

## Status

The newest published MCP protocol revision is `2026-07-28`. It is currently published as a release candidate, while `2025-11-25` remains the latest finalized specification release.

This repository serves both revisions on the same Streamable HTTP endpoint:

- `2026-07-28` requests use stateless, self-describing request metadata, `Mcp-*` routing headers, and `server/discover`.
- Legacy clients continue to negotiate through `initialize` and may use transport session IDs.
- Protocol selection is per request. The server must not be globally forced into legacy-only or stateless-only mode unless a deployment has a specific compatibility constraint.

## Implementation decision

Upgrade `github.com/mark3labs/mcp-go` from `v0.58.0` to `v1.0.0-beta.1`.

This is the smallest compatible upgrade for the existing implementation because the beta adds `2026-07-28` support while preserving the server, tool, hook, and Streamable HTTP APIs already used by this repository. Migrating the entire service to another SDK is not required to update the wire protocol and would create a much broader, unrelated change.

Do not add `WithStateLess(true)` globally. The SDK automatically serves `2026-07-28` stateless requests without session IDs and preserves legacy session behavior for older clients.

## Acceptance criteria

The repository-level protocol tests must prove that:

1. `server/discover` accepts a valid `2026-07-28` request and advertises `2026-07-28` first.
2. Modern responses do not mint an `Mcp-Session-Id`.
3. `tools/list` returns the modern `resultType`, cache hints, and the configured tool catalog.
4. A `2025-11-25` `initialize` request still succeeds.

## Primary sources

- MCP specification releases: <https://github.com/modelcontextprotocol/modelcontextprotocol/releases>
- MCP `2026-07-28` changelog: <https://modelcontextprotocol.io/specification/2026-07-28/changelog>
- Official Go SDK `v1.7.0` release notes: <https://github.com/modelcontextprotocol/go-sdk/releases/tag/v1.7.0>
- `mcp-go v1.0.0-beta.1` release: <https://github.com/mark3labs/mcp-go/releases/tag/v1.0.0-beta.1>
- `mcp-go` protocol migration guide: <https://github.com/mark3labs/mcp-go/blob/v1.0.0-beta.1/www/docs/pages/protocol-2026-07-28.mdx>
