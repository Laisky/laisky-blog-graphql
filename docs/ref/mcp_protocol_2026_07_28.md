# MCP protocol 2026-07-28 compatibility

Last verified against upstream releases: 2026-09-29

## Status

The latest finalized MCP protocol revision is `2026-07-28`. This repository
already implemented that wire revision with `mcp-go v1.0.0-beta.1`; the September
upgrade moves to the stable `v1.1.1` release, published on 2026-09-23. It is a
stabilization and dependency-compatibility update, not a new protocol rollout.
The PR review also reproduced and repaired a legacy session ownership boundary;
that application fix is distinct from the dependency version change.

The same Streamable HTTP endpoint serves modern and legacy clients:

- Modern requests carry protocol/client metadata and `Mcp-Method` / `Mcp-Name`
  routing headers. They use `server/discover` without an initialize handshake
  or transport session ID.
- Tool catalogs use only the current request's credentials. Modern results
  preserve `resultType`, `ttlMs` and private cache scope, including after user
  preference filtering. A legacy session ID never supplies an identity.
- Legacy clients negotiate with `initialize`, send `notifications/initialized`,
  and use the returned transport session ID. Authenticated sessions are bound
  to the same API key on every request. The acceptance matrix includes
  `2025-03-26`, `2025-06-18` and `2025-11-25`.

## Implementation decision

Keep `github.com/mark3labs/mcp-go` and upgrade it from `v1.0.0-beta.1` to `v1.1.1`.
The upstream stable releases preserve the server, tool, hook and Streamable
HTTP APIs used here. They also include fixes for result-envelope parsing,
transport lifecycle handling, task error handling and schema property order.
Migrating the application to a different SDK is unnecessary for this update.

Do not add `WithStateLess(true)` globally. The SDK selects modern stateless
behavior per request while preserving legacy sessions. Keep identity
normalization and tool-preference filtering in the request path, with no
session-to-credential cache or missing-credential fallback.

### Legacy client rollout

After rollout, authenticated legacy clients must initialize a new session and
send the same API key on POST requests, notifications, GET and DELETE. Previously
issued unsigned IDs are intentionally rejected for authenticated requests.
Changing credentials, including upgrading an anonymous connection to an
authenticated one, requires reinitialization. Do not automatically replay a
state-changing tool call while recovering a session.

Session proofs bind a random nonce to the request credential with domain-separated
HMAC-SHA256. They do not replace tool-level authorization or billing, and DELETE
cleans up transport state rather than maintaining a durable revocation list.
Modern requests remain stateless; anonymous public discovery remains available.
See [session ownership](pr51_session_isolation_2026_09_29.md) for the complete
security contract, compatibility boundary and regression matrix.

SDK support is not an application feature declaration: this change does not
add persistent task storage, an OAuth authorization server, sampling,
elicitation, new resources/prompts or application multi-round-trip workflows.
Such features need their own requirements, authorization and acceptance tests.
The browser's existing dual-era transport is retained; unrelated frontend or
application SDK major-version migrations are not part of this Go dependency
refresh.

## Acceptance matrix

`internal/mcp/protocol_2026_07_28_test.go`,
`internal/mcp/protocol_sdk_upgrade_test.go` and the `TestMCPReview` suites exercise
the production HTTP handler. They use raw JSON-RPC requests rather than relying
on an SDK client to make both sides agree on the same accidental behavior.

| Contract | Evidence |
| --- | --- |
| Modern discovery, version ordering and identity | `TestServerSupportsMCPProtocol20260728` |
| Modern stateless tool list, complete envelope and private cache hints | Existing protocol suite and `TestMCPStableSDKCatalogIsolation` |
| Success and application-error tool calls | `TestMCPStableSDKToolCalls`; only a leaf tool dependency is substituted |
| Missing/mismatched routing headers | `TestMCPStableSDKRejectsMisroutedRequests`; asserts `-32020` and zero leaf invocations |
| Unsupported version | `TestMCPStableSDKRejectsUnknownVersion`; asserts the wire code `-32022` |
| Alternating caller identities | `TestMCPStableSDKCatalogIsolation`; A/B/A/B must not reuse the other caller's preferences |
| Legacy initialize, notification and tools/list | `TestMCPStableSDKLegacyRoundTrips` for three protocol revisions |
| Legacy session ownership, tampering, canonical credentials and replica independence | `TestMCPReview` suites; 20 race-enabled repetitions in PR CI |
| Modern requests never inherit legacy identity; modern GET/DELETE remain unsupported | `TestMCPReviewModernCatalogNeverInheritsSessionIdentity` and `TestMCPReviewModernHTTPMethodsStayUnsupported` |

Run the full regression suite, not only the protocol subset:

```sh
go mod verify
go test -race -count=20 -timeout 5m ./internal/mcp -run TestMCPReview
go test -race -cover -timeout 5m ./...
go build ./...
go vet ./...
make lint-pure-go lint-system-owner
```

## Coupled dependency migrations

The first broad upgrade was compiled and tested before compatibility fixes.
It exposed two real source/API incompatibilities:

1. `pdfcpu v0.16.0` requires `context.Context` for `PageCount`, `Bookmarks` and
   fixture generation via `Create`. Forward caller cancellation; do not turn
   cancellation into an empty successful outline. `TestPDFParserCanceledContext`
   covers cancellation/deadline errors, and the existing PDF pipeline tests
   cover ordinary parsing. Fixture generation must fail tests rather than skip.
2. `pgx v5.11.0` adds `Rows.TypeMap()`, which `pgxmock/v4 v4.9.0` does not
   implement. Migrate the test-only import to `pgxmock/v5 v5.2.0`, whose release
   explicitly supports pgx 5.11 and TypeMap. Keep the production pgx major version.

`make gen` now uses the gqlgen version selected by `go.mod`, rather than a
second hardcoded version that drifts from the runtime dependency. Generated
GraphQL artifacts must come from that command, never manual edits.

The root Go language minimum remains 1.27.0. The production Docker builder
uses the current 1.27.1 patch release.

## Primary sources

- [MCP latest specification](https://modelcontextprotocol.io/specification/latest)
- [MCP 2026-07-28 changelog](https://modelcontextprotocol.io/specification/2026-07-28/changelog)
- [mcp-go v1.1.1 release](https://github.com/mark3labs/mcp-go/releases/tag/v1.1.1)
- [mcp-go beta-to-stable changes](https://github.com/mark3labs/mcp-go/compare/v1.0.0-beta.1...v1.1.1)
- [pdfcpu v0.16.0 API](https://github.com/pdfcpu/pdfcpu/tree/v0.16.0/pkg/api)
- [pgxmock v5.2.0 release](https://github.com/pashagolub/pgxmock/releases/tag/v5.2.0)
- [Official Go downloads](https://go.dev/dl/)
