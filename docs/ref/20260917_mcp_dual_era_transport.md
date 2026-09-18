# MCP browser transport: verified protocol references

Research date: 2026-09-17. This implementation is a tools-only Streamable HTTP
client; it is not a complete replacement for a general-purpose MCP SDK.

## Primary sources

- [2026-07-28 Streamable HTTP](https://modelcontextprotocol.io/specification/2026-07-28/basic/transports/streamable-http)
- [2026-07-28 versioning](https://modelcontextprotocol.io/specification/2026-07-28/basic/versioning)
- [2025-11-25 transport](https://modelcontextprotocol.io/specification/2025-11-25/basic/transports)
- [2025-06-18 lifecycle](https://modelcontextprotocol.io/specification/2025-06-18/basic/lifecycle)
- [Official TypeScript SDK error reference](https://github.com/modelcontextprotocol/typescript-sdk/blob/main/docs/servers/errors.md)

The repository's `internal/mcp/protocol_2026_07_28_test.go` already describes
modern `server/discover`/`tools/list` requests and legacy initialize behavior.
No protocol upgrade is imposed on the backend by this browser change.

## Deliberate boundary

Modern requests contain per-request protocol/client metadata and routing headers,
without a legacy initialize handshake or session. Tool schemas are discovered
and validated before calls, including static primitive `x-mcp-header` bindings.
Illegal, duplicate, dynamically located or unsupported annotations exclude a
tool; header values are safely encoded and never lossy integer conversions.

The client only uses legacy initialize after a permitted, bounded discovery
rejection. Recognized modern version/header/capability/method failures do not
cause a downgrade. A truncated HTTP diagnostic is not sufficient evidence for
fallback. Legacy session IDs remain optional; a negotiated legacy client sends
`notifications/initialized` before operations.

Both paths accept JSON or incremental, request-scoped SSE. Response IDs must
match exactly. SSE comments, notifications, UTF-8 chunk boundaries, CR/LF/CRLF,
empty priming events and multiline data are handled. Responses are bounded to
64 MiB and requests have finite deadlines. Interrupted streams do not trigger
re-POST/resume of possibly committed tools. Modern cancellation closes the
stream; legacy cancellation additionally sends a bounded best-effort notice.

No sampling, elicitation, roots, task, subscription, or interactive continuation
capabilities are advertised. Unsupported server-initiated work is rejected.
`input_required` is not completed automatically. The oldest separate GET/SSE
transport is not implemented. Catalogs are per credential, bounded and TTL-based;
this is not a notification subscription client. Accepted legacy revisions are
2025-11-25, 2025-06-18 and 2025-03-26 over Streamable HTTP where supported.

There is intentionally no automatic tool retry for 404/session expiry, network
errors, 5xx, JSON-RPC failures, incomplete SSE or schema/header mismatch. The next
explicit action may renegotiate or refresh discovery, but the user/client must
first reconsider state and concurrency preconditions.
