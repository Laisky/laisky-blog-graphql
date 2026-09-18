# MCP, GraphQL and Web: shared functions, independent interfaces

Updated: 2026-09-17. Base reviewed: `9b070df6c6281c2ed73e5e1d5d5f60852e232261`.
This follow-up builds on the merged FileIO work in #45, #46 and #47.

## Architectural decision

MCP and GraphQL are **peer external interfaces** over underlying application
functions. Neither is the primary interface or a prerequisite for the other.
Reuse authentication, input validation and business services; do not implement
GraphQL by invoking an MCP handler or inheriting MCP registration switches.

`settings.mcp.tools.*.enabled` controls MCP exposure only. Disabling
`web_search`, `web_fetch` or `extract_key_info` in MCP must not disable the
corresponding GraphQL field, prevent its shared service from being constructed,
or hide the browser page that calls GraphQL. A missing underlying dependency can
make that function unavailable through either adapter; this is not an MCP gate.
No new GraphQL enable switch is introduced in this change.

The earlier candidate proposal to reuse MCP switches across interfaces is
**rejected and is not part of this implementation**. In particular, no
`WithAvailability` or `WithEnabled` bridge is added to GraphQL construction.
`internal/web/resolver.go` is unchanged. The shared RAG service's own configuration
is distinct from `MCPToolsSettings.ExtractKeyInfoEnabled`.

The runtime response retains `tools` for the actual MCP registry and `consoleTools`
for browser navigation. Search, fetch and extraction pages use configured
GraphQL dependency availability, regardless of those MCP flags. Pages that
actually invoke MCP, such as FileIO and memory, still need that transport for
those operations. This describes their existing implementation, not a rule that
MCP owns the underlying function. The browser uses `consoleTools`, never a
fallback to MCP flags to determine availability of GraphQL pages.

## Current entrypoint inventory

| Capability | MCP | GraphQL | Browser and remaining differences |
| --- | --- | --- | --- |
| Search | `web_search` | `WebSearch` | Page calls GraphQL; same provider, canonical API key and query rules; GraphQL retains metadata, MCP retains its smaller result envelope. |
| Rendered fetch | `web_fetch` | `WebFetch` | Page calls GraphQL. Shared pre-billing URL admission; GraphQL currently always requests Markdown, while MCP can select HTML. |
| Context extraction | `extract_key_info` | `ExtractKeyInfo` | New dedicated GraphQL page, router entry, menu item and homepage link; server owns size/top-K limits. |
| FileIO content/lifecycle | Seven FileIO tools | No fields yet | Browser uses MCP for file operations and separate HTTP for editor/history. Version-aware behavior and plugin routing were added by the preceding PRs. |
| File history | `file_list_versions`, `file_read_version`, `file_restore_version` | No fields yet | Bounded MCP metadata paging and condition-protected plugin restore; existing HTTP/browser history remains available. Go integration acceptance is still pending. |
| Memory lifecycle | Four memory tools | No fields yet | Existing page calls MCP; GraphQL coverage remains an open capability gap. |
| Human directives | Agent receives/consumes through MCP | No equivalent fields | Human queue-management UI uses HTTP. Producing and consuming directives are intentionally different roles. |
| Tool discovery/pipelines | `tools/list`, `find_tool`, `mcp_pipe` | GraphQL introspection describes its own fields | MCP Inspector is available; transport-specific discovery need not use the same envelope. |
| Blog, SSO and administration | Not automatically exposed | Existing domain-specific fields | Do not make privileged operations agent tools merely to equalize field counts. |

A schema test proving that FileIO fields do not exist is a boundary test, **not**
a successful feature-parity test. The missing GraphQL adapters remain open below.

## Implemented issue checklist

Checked means the code change is included. Regression coverage and actual
execution are reported separately; checked does not mean full integration
acceptance or production deployment.

- [x] **I01 — Independent interfaces:** remove the candidate MCP-to-GraphQL gate,
  retain independent GraphQL construction and add production-resolver-builder
  regression tests for both MCP flag states.
- [x] **I02 — Browser availability:** separate `consoleTools` from `tools` so
  disabling an MCP tool cannot hide its functioning GraphQL page. Test all four
  combinations of MCP enabled/disabled and GraphQL configured/unconfigured.
- [x] **I03 — Canonical identity:** GraphQL search/fetch use the shared
  `mcpauth` context/header normalization rather than an independent Bearer parser.
  Existing GraphQL extraction already used that identity path.
- [x] **I04 — Query validation:** shared trimmed, non-empty, valid UTF-8 queries
  with a 16,384-byte limit before billing/provider effects; reject malformed input
  consistently across search and extraction adapters.
- [x] **I05 — Typed MCP arguments:** extraction rejects fractional/string/bool
  `top_k` and the undocumented `topK` alias; fetch accepts an actual Boolean for
  `output_markdown` rather than silently coercing strings/numbers. Absent/null
  optional values select defaults. Published extraction schema uses integer bounds.
- [x] **I06 — Result integrity:** normalize empty search/extraction collections to
  arrays and reject a nil search-provider result instead of dereferencing it.
- [x] **I07 — Fetch admission parity:** both adapters use the same bounded,
  cancellation-aware URL/DNS admission before billing. Reject non-HTTP(S), URL
  credentials, local/non-public addresses and ambiguous numeric host forms.
- [x] **I08 — Fetch lifecycle/logging:** URL logging removes userinfo/path/query/fragment
  and redacts malformed inputs. Crawler waiting observes cancellation; failed
  tasks with missing failure metadata no longer require pointer dereferences.
- [x] **I09 — Deployment paths:** introduce the explicit `publicApiBasePath` and
  use it for GraphQL and dedicated tool HTTP paths. Keep API mount, SPA route base
  and an explicitly configured remote MCP endpoint distinct.
- [x] **I10 — Browser request handling:** preserve structured GraphQL errors,
  numeric/string error paths and HTTP status, support cancellation, and never
  automatically retry a mutation. Search/fetch isolate late execution results
  after credentials change, lock, or unmount and block overlapping submissions.
- [x] **I11 — Extraction UI:** add a dedicated page through the existing GraphQL
  field, with inputs, optional top-K, results/errors, request cancellation,
  navigation and a working homepage link.
- [x] **I12 — Browser protocol headers:** explicitly allow Authorization,
  conditional write and MCP headers; expose ETag and MCP session/protocol headers.
  Preserve the existing allowed-origin policy and Vary values.
- [x] **I13 — Single-capability MCP initialization:** include Redis fetch, memory,
  pipeline and discovery-only configurations in the outer server initialization
  condition. This does not move GraphQL registration under that condition.

## Regression coverage and validation status

New adapter tests compare admission, canonical keys, provider invocation counts,
empty results and one charge per accepted entrypoint. URL policy tests use
controlled DNS responses and contain no actual renderer or billing call. The
GraphQL independence test invokes the **production resolver builder** with MCP
switches on/off and confirms invalid inputs reach shared validation, rather than
being rejected by an unrelated transport gate; it runs before billable effects.
It is not a generated GraphQL HTTP end-to-end test.

| Original implementation validation (`718c653`); continuation results are linked below | Result |
| --- | --- |
| Shared toolpolicy package, Go 1.23.2 `-race -count=3` | PASS |
| Independent availability and CORS helpers/tests, standalone Go `-race -count=3` | PASS |
| Actual API-base and GraphQL-client modules in Node 22, 42 assertions repeated three times | PASS; fetch/auth dependencies mocked |
| Standalone strict TypeScript 5.8.3 check of `api-base.ts` | PASS |
| Syntactic transpilation of 13 TypeScript/TSX files and formatting of all changed Go files | PASS; not full type checking or Go compilation |
| Full Go 1.27 repository compile, race tests, full lint | Not run in the dependency-limited authoring environment |
| Actual MCP/GraphQL HTTP, PostgreSQL, full React/Vitest, Vite build | Not run; tests provided are not evidence of a passing integration run |

Earlier FileIO checks do not validate these later changes. Do not equate
standalone helper execution or syntactic transpilation with an application build.
Use the repository's existing test/lint commands in a dependency-ready checkout:

```sh
go test -race -cover ./...
make lint
cd web
pnpm install --frozen-lockfile
pnpm run lint
pnpm run test
pnpm run build
```

## Gap status

- [x] **G01 — typed GraphQL FileIO and memory:** `internal/web/fileio/schema.graphql`
  publishes `FileStat`/`FileRead`/`FileList`/`FileSearch`/`FileListVersions`/
  `FileReadVersion`/`MemoryListDirWithAbstract` as queries and `FileWrite`/
  `FileDelete`/`FileRename`/`FileRestoreVersion`/`MemoryBeforeTurn`/
  `MemoryAfterTurn`/`MemoryRunMaintenance` as mutations, with regenerated gqlgen
  output. Resolvers live in `internal/library/fileio`. They authenticate through
  `mcpauth.FromContextOrHeader`, map typed `files.Error`/`mcpmemory.Error` codes
  onto GraphQL error extensions, and route every mutation through the shared
  `mcptools.ConditionalFileService` gate, which MCP now also calls. Byte counts
  and offsets use the new exact `BigInt` scalar; history IDs stay decimal
  strings. Cross-interface contract tests execute real GraphQL operations and
  assert that a mutation missing `expected_version`/`create_only` never reaches
  storage at all, distinguishing "rejected before storage" from "rejected by
  storage".
- [x] **G02 acceptance:** Executed against live PostgreSQL 17 with pgvector
  0.8.6. `go test -race -shuffle=on -count=3 ./internal/mcp/files -run
  '^TestFileIOHistory'` and the whole `internal/mcp/files` package pass, with
  `FILEIO_TEST_POSTGRES_DSN`/`MCP_FILES_TEST_POSTGRES_DSN` set, so the
  Postgres-gated cases ran instead of skipping.
- [x] **G03 — fetch format selection:** `WebFetch(url, output_markdown)` accepts
  the same selection as the MCP tool through a real schema and regenerated
  resolver change, and `WebFetchResult.output_markdown` reports the format that
  was actually requested. The console exposes a Markdown/Raw HTML selector and
  renders the body as text in both formats. A parity test asserts MCP and
  GraphQL request the same format for the same caller intent.
- [x] **G04 client implementation and acceptance:** Modern/legacy Streamable HTTP
  lifecycle, optional legacy sessions, required metadata/header mirroring,
  bounded JSON/SSE, cancellation and no automatic tool replay are implemented.
  The full frontend suite now executes: eslint clean, 310/310 vitest, `tsc -b`
  and the production `vite build` all pass.
- [~] **G05 — crawler egress:** The in-repository half is implemented and
  tested. Admission publishes the exact addresses it validated, the crawl task
  and the GraphQL worker API carry an `EgressPolicy` (pinned host/addresses,
  redirect budget, subresource decision), and every origin the renderer reports
  is re-admitted before any body is returned, so a redirect escape or a rebound
  host fails closed. `settings.mcp.tools.web_fetch.egress.require_verified`
  rejects an unreported chain. **The renderer is a separate service that is not
  in this repository**, so nothing here proves it pins its sockets or reports
  truthfully. See [the egress contract](crawler_egress_policy.md).
- [x] **G06 — billing/audit reconciliation:** `CheckUserExternalBilling` now
  returns a classified error, so accepted/denied/unknown/not_attempted are
  distinguishable. The recorded cost follows the billing outcome rather than the
  tool result, both interfaces write the classification under the reserved
  `_billing` parameters key, and GraphQL now audits a denial instead of
  returning silently. See [the matrix](entrypoint_billing_audit_matrix.md).
  Per-user authorization and live backend health remain outside the configured
  adapter catalog by design.
- [x] **G07 — full repository and frontend execution:** Go 1.27.1. `go build
  ./...`, `go vet ./...`, `go test -race -cover -shuffle=on ./...`,
  `govulncheck ./...`, `check_pure_go.sh` and `check_system_owner.sh` all pass.
  `golangci-lint` reports 351 findings, down from 371 on the merge base, and
  **zero** in any file this branch touches; the remainder is pre-existing
  repository-wide debt outside this change's scope. Frontend: eslint, vitest,
  `tsc -b` and `vite build` all pass.
- [x] **G08 acceptance:** The committed real-handler/router and shared-hold
  tests execute as part of the full `internal/web` and `internal/mcp` runs above.

## Defects found and fixed while closing these gaps

Each was reproduced with a failing behavior test first, then fixed, and the test
is retained as a regression:

- **GORM prepared-statement deadlock** (`internal/web/blog/oneapi/db.go`). With
  `PrepareStmt: true` and a single-connection SQLite pool, GORM holds its
  statement-cache mutex across `database/sql.PrepareContext` while a goroutine
  inside a transaction owns the only connection and needs that same mutex. The
  process hung permanently; the full `go test -race -cover ./...` run surfaced it
  as a 10-minute timeout. The reproduction fails deterministically (30s deadline,
  3/3) and passes in ~1s after the fix, which disables the statement cache for
  any pool below `minPrepareStmtPoolSize`. Upstream `go-gorm/gorm#7350` and
  `#7465` are still open with no released fix.
- **Startup panic without telegram configuration**
  (`internal/web/telegram/controller/throttle.go`). `web.NewResolver` builds a
  fallback telegram controller when `cmd/api.go` could not create the telegram
  service and the `telegram` task was not requested. That constructor panicked
  on a missing throttle configuration, taking down GraphQL, MCP and the HTTP
  routes over an optional subsystem — contradicting the documented "log an error
  and keep serving" contract. It now degrades, the alert path refuses to send
  rather than dereferencing nil, and `cmd/api.go` still fails fast when the
  telegram task is explicitly requested.
- **Stale URL-redaction test** (`internal/mcp/tools/behavior_test.go`) still
  asserted that log fields keep the path and echo malformed input, which the R01
  fix intentionally changed. It now pins the shipped contract exactly.
- **Browser API paths** addressed `/tools/...` while the server mounts
  `<prefix>/tools/...` whenever a public prefix is configured; the tests encoded
  the stale expectation.
- **Dead code and lint regressions** the earlier refactors left behind: six
  unused functions, five unclassified `nilerr` returns, two dead
  `gmw.GetLogger` nil checks, a shadowed `copy` builtin, and control characters
  inside regular-expression literals that failed eslint.

## Completion follow-up

See [the continuation record](pr49_completion_20260917.md) for the earlier
red/green results, operation-level inventory and the historical blockers, and
[the 2026-09-18 follow-up](pr49_followup_20260918.md) for the catalog/preflight
budget regressions and configured-price display.

## Scope and rollout

No CI workflow/job/step, benchmark trigger, dependency, lockfile or database
migration is modified, and no benchmark is dispatched. The generated GraphQL
files (`internal/web/generated.go`, `internal/library/models/models.go`) ARE
regenerated, because G01 and G03 add real schema fields; they were produced by
`make gen` (gqlgen v0.17.94) and never hand-edited. The change does not merge
itself or deploy a new service/browser bundle.

Deploy server and browser configuration together. `publicApiBasePath` is the
public API mount; it is not a site-specific SSO/SPA router path. Missing runtime
metadata uses the documented local prefix heuristic/default console values, not
MCP flags as authorization for GraphQL. Authorization and service availability
remain enforced by the server, not by hidden navigation links.
