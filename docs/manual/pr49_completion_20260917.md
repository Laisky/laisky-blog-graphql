# PR #49 continuation: implementation and acceptance ledger

Date: 2026-09-17. Reviewed remote head:
`bda05b81c930a32c17ea67a5eec620dc43b91b3c` on
`fix/entrypoint-parity-independent-interfaces-20260917`.

## Delivery status

This continuation is included in the commit containing this document on PR #49.
It was published as a fast-forward addition to
`fix/entrypoint-parity-independent-interfaces-20260917`, based on `bda05b8`.
The former downloadable patch is no longer the only delivery of this work.

Before publication, all 13 replaced source files were checked against their
Git blob identities at the remote base. All 35 intended changes were rendered
and Go sources formatted together. This was a partial, hash-verified source
staging area, not a dependency-ready full repository checkout. Full application
build and integration acceptance remain outstanding as listed below.

The PR checklist separates code included, targeted behavior tests executed,
and outstanding acceptance. No merge or deployment is part of this update.

## Architectural invariants

MCP and GraphQL remain peer interfaces over shared application functions.
Disabling an MCP tool does not disable a GraphQL field or a separately registered
HTTP handler. Authentication and business preconditions remain server-enforced;
visibility in a catalog or browser menu is not an authorization decision.

No CI, automatic benchmark trigger, dependency, repository module version,
database migration or generated GraphQL file is changed. No paid provider,
production mutation, benchmark dispatch, merge or deployment was used.

## Remaining-item status

| ID | Implementation in this continuation | Acceptance / remaining work |
| --- | --- | --- |
| G01 | No new GraphQL FileIO/memory fields are claimed. | Still open. Requires real schema/model/resolver changes, gqlgen regeneration and cross-interface auth/CAS/error tests. The existing negative schema tests prove absence, not parity. |
| G02 | Three MCP history adapters, bounded keyset metadata pagination, exact decimal IDs, selected-plugin restore using the original CAS condition, registration and public guidance. | Source is included. Real Go, SQLite, PostgreSQL, RAG and PageIndex integration tests are authored but not executed. Do not mark full acceptance complete. |
| G03 | Existing GraphQL/browser Markdown-only fetch contract is unchanged. | Still open. A real schema/resolver/codegen change is needed; no generated file was edited by hand. |
| G04 | Tools-only modern and legacy Streamable HTTP client, JSON/SSE parsing, version/header/catalog handling, cancellation and no implicit mutation replay. | 42 targeted tests pass in three runs. Full Vitest/browser and real Go-server interoperability remain unverified under G07. |
| G05 | No claim of crawler-side egress enforcement. | Still open. A separate crawler must validate connection pinning, redirects and subresources; admission DNS checks do not establish that result. |
| G06 | Actual registered-tool/configured-adapter metadata, a per-operation billing/audit inventory, and additional logging-copy redaction fixes. | Partial. Catalog helper and redaction tests pass. Per-user catalog filtering, live backend health, billing-outcome reconciliation and real audit persistence tests remain open. |
| G07 | A Go 1.27 upgrade and dependency downloads were attempted; independent available tests were executed. | Still open. Go 1.27, full repository/race/lint, PostgreSQL, GraphQL HTTP, React/Vitest and the production frontend build were not executed successfully. |
| G08 | Dedicated HTTP registration is outside the MCP factory/success branch. The application owns shared human-request holds. | Source and actual-handler tests are included; dependency-ready Go execution is still required. |

Checked implementation must not be confused with accepted deployment. Only G04's
bounded client implementation can be marked implemented-and-target-tested here;
its application-level acceptance is still part of G07.

## Behavior-first evidence

### Browser MCP transport

The same committed test source was run against the exact original client blob
`4a230db6c99916242c29e01baf1f240913cce532` and the replacement. Tests use real Node
`Response` / `ReadableStream` objects and the production client implementation.
Only deployment/auth plumbing and network responses are fixtures. Vitest's
runner import is adapted to Node's native test runner; this is not a complete
Vitest or React application test run.

There are 38 client lifecycle/transport cases plus four new header-helper cases.
The combined old-client run had 10 passes and 32 failures. The four header-helper
cases use the new helper in both phases, so the precise old-client result is
**6 passes / 32 failures among 38 client cases**, not 42 old-client regressions.
All 42 cases pass in each of three post-fix runs: 126 passing case executions,
no failures, skips or cancellations. These are cases, not 32 distinct defects.

The coverage includes modern discovery and per-request metadata; legacy
initialize/initialized and optional session IDs; JSON/SSE chunk and newline
handling; exact response ID matching; concurrent callers; cancellation; bounded
responses; invalid catalogs/headers; and refusal to automatically replay a tool
call after uncertain completion. The client advertises no sampling, elicitation,
roots, tasks or subscriptions, and does not implement the older separate GET/SSE
transport. See [the protocol references](../ref/20260917_mcp_dual_era_transport.md).

### Browser CORS headers

A behavior test against the exact old helper blob
`23094654886eac8a078e9a5812c8e5a858abe72c` fails because `Mcp-Method` is missing.
The replacement permits the required routing headers and bounded, syntactically
valid `Mcp-Param-*` names only after the existing origin decision. It does not
accept arbitrary requested headers. The helper tests pass with race detection,
shuffled order and ten repeats, with 95.8% helper statement coverage.

This isolated helper run is not a Gin-router or real-browser CORS acceptance run.

### Raw MCP and persisted-argument redaction

The previous origin-only `URLForLog` fix did not ensure every logging path used
it. New tests reproduce raw `tools/call` URLs, modern memory arguments, nested
pipeline arguments and malformed/truncated JSON retaining synthetic secrets.
Two of the three pre-fix top-level tests fail on each of three runs; the third
checks that useful diagnostic metadata survives. The fixed implementation adds
an audit-parameter-copy test; all four top-level tests pass across ten shuffled
race-enabled runs, with 84.7% coverage of the targeted redactor package closure.

The pre-fix closure contains exact repository files, verified by Git blob hash:

| Source | Original Git blob |
| --- | --- |
| `internal/mcp/log_redaction.go` | `37a9869e0e0576a37177df4da5d5815131087802` |
| `internal/mcp/files/logging_redaction.go` | `4da12024833441211f526aa3bccdf2c21a38e70e` |
| `internal/mcp/memory/logging_redaction.go` | `a668c179e17a5f2e74ea877f645242ba75ca16bc` |

The post-fix closure uses the new redactor and the existing `URLForLog` function
copied verbatim from the reviewed policy file. It does not simulate successful
redaction. It also does not start a real logger, HTTP server or database. Original
operation arguments are checked for non-mutation. Free-form upstream errors and
other logging sinks are not claimed to have been comprehensively sanitized.

## Validation actually executed

Toolchains: Go 1.23.2, Node 22.16.0 and TypeScript 5.8.3.

| Check | Result and scope |
| --- | --- |
| Client red-to-green run | Old combined run: 10 pass / 32 fail; new: 42 pass in each of three runs. Details above avoid conflating new header tests with old-client failures. |
| Strict TypeScript of transport and header production modules | PASS; not whole-frontend type checking. |
| Configured-catalog helpers + existing console-availability helper | PASS: `-race -shuffle=on -count=10`, `go vet`; 95.7% statement coverage of this isolated closure. |
| CORS helper | PASS: same race/shuffle/repeat settings and `go vet`; 95.8% isolated helper coverage. |
| Logging-copy redactor | Pre-fix fails as above; post-fix race/shuffle/ten repeats and `go vet` PASS; 84.7% closure coverage. |
| Complete local Go overlay source formatting | PASS on 16 files. This is parsing/formatting, not repository compilation or type checking. |
| Patch-applier safety cases | 10 PASS: source mismatch, changed target, new-file collision, symlink/path escape, metadata/CI path rejection, integrity checks and preserving unrelated content. |
| Full repository / generated GraphQL / PostgreSQL / React / production build | NOT EXECUTED successfully. No earlier PR result substitutes for these changes. |

The catalog closure copies the unchanged console helper with Git blob
`a526bec4ea53df900ee433e444f86561fa8530cf`; adapter/source-to-resolver wiring tests
are provided but were not executed. Percentages are closure-level statement
coverage, not project-wide coverage or production reliability measurements.

## Go 1.27 installation attempt

The [official Go downloads metadata](https://go.dev/dl/?mode=json) lists Go 1.27.1.
The advertised linux-amd64 archive checksum is
`63d339f0da5ab53635a56f2490a7984dfe12dfcff22ad749f63edaf590168445`.
This is a published checksum, **not** a downloaded-and-verified local archive.

`GOTOOLCHAIN=go1.27.1 go version` attempted the official toolchain download and
failed resolving `proxy.golang.org` (DNS connection refused). Official archive
and repository download attempts also failed. The environment still reports
`go version go1.23.2 linux/amd64`; no successful Go 1.27 installation is claimed.
The repository's `go.mod` was not lowered to make an old compiler appear valid.

## Next executable acceptance steps

Check out the continuation commit on PR #49 in a clean, isolated worktree.
Do not apply an older downloadable patch over a newer branch head. Run the
following with the repository toolchain and dependencies installed:

```sh
go test -race -shuffle=on -count=3 ./internal/mcp/files -run '^TestFileIOHistory'
go test -race ./internal/mcp ./internal/web ./internal/mcp/tools ./internal/mcp/files
# Set FILEIO_TEST_POSTGRES_DSN to a disposable PostgreSQL+pgvector database first.
go test -race -cover ./...
make lint
cd web
pnpm install --frozen-lockfile
pnpm run lint
pnpm run test
pnpm run build
```

Commands are acceptance instructions, not execution claims. History and HTTP
routing tests still require complete code compilation and actual dependencies.
See [the history contract](mcp_file_history.md) and
[the operation/billing matrix](entrypoint_billing_audit_matrix.md) for behavior
and deliberately unresolved reconciliation concerns.
