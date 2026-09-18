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

## Remaining gaps — not marked fixed

- [ ] **G01:** Add typed GraphQL FileIO and memory adapters over shared services,
  with their own authentication, error mapping, concurrency preconditions and
  properly regenerated gqlgen output; test against the other entrypoints.
- [ ] **G02 acceptance:** History adapters, bounded string-ID pagination, plugin-aware
  conditional restores, registration and tests are included. Execute the real
  Go/RAG/PageIndex/PostgreSQL tests before closing acceptance.
- [ ] **G03:** Add GraphQL/browser fetch format selection via a real schema and
  generated-resolver change; current GraphQL Markdown-only behavior is retained.
- [x] **G04 client implementation:** Modern/legacy Streamable HTTP lifecycle,
  optional legacy sessions, required metadata/header mirroring, bounded JSON/SSE,
  cancellation and no automatic tool replay are implemented. The same behavior
  tests fail on the prior client and pass on the replacement. Real-server and
  full-frontend acceptance remain part of G07.
- [ ] **G05:** Validate crawler-side connection pinning, redirects and subresource
  requests. Admission DNS checks alone do not establish end-to-end SSRF safety.
- [ ] **G06 acceptance:** An operation-level configured-adapter catalog and
  billing/audit matrix are included. Per-user authorization, backend health and
  full billing/audit parity remain separate; do not treat static cards or this
  metadata as successful live execution.
- [ ] **G07:** Execute full repository/frontend and real transport/database tests.
  The independent helper suites above are only a subset of acceptance.
- [ ] **G08 acceptance:** Dedicated HTTP construction now runs outside MCP
  initialization, with application-owned shared holds. Real handler/registry
  tests are included but must run with repository dependencies before acceptance.

## Completion follow-up

See [the continuation record](pr49_completion_20260917.md) for exact red/green
results, the attempted Go 1.27 upgrade, operation-level inventory and remaining
blockers. New code is not proof of an executed Go integration test. In particular,
G01/G03 require real gqlgen output, G05 requires crawler-side verification, and
G07 still requires the configured dependency/toolchain environment.

## Scope and rollout

No CI workflow/job/step, benchmark trigger, dependency, lockfile, database
migration or generated GraphQL file is modified. No benchmark is dispatched.
The change does not merge itself or deploy a new service/browser bundle.

Deploy server and browser configuration together. `publicApiBasePath` is the
public API mount; it is not a site-specific SSO/SPA router path. Missing runtime
metadata uses the documented local prefix heuristic/default console values, not
MCP flags as authorization for GraphQL. Authorization and service availability
remain enforced by the server, not by hidden navigation links.
