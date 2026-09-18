# PR #49 — closing the outstanding checklist

This round closes the items that earlier rounds left unchecked, and records what
was actually executed rather than what was merely written. Where a claim cannot
be executed inside this repository, that is stated instead of implied.

## Toolchain actually used

| Component | Version | Note |
| --- | --- | --- |
| Go | 1.27.1 | `go.mod` already required 1.27.0. Earlier rounds only had 1.23.2, which is why G07 stayed open. |
| Node | 22.16.0 | |
| TypeScript | 5.9.3 | |
| pnpm | 12.3.4 | |
| PostgreSQL | 17 with pgvector 0.8.6 | Disposable container, used to run the previously skipped Postgres-gated tests. |

## Executed results

| Check | Result |
| --- | --- |
| `go build ./...` | pass |
| `go vet ./...` | pass |
| `go test -race -cover -shuffle=on -count=1 ./...` | pass, whole repository |
| `go test -race -shuffle=on -count=3 ./internal/mcp/files -run '^TestFileIOHistory'` | pass, against live PostgreSQL + pgvector |
| `go test -race ./internal/mcp/files` (full package, Postgres DSN set) | pass; the `Postgres` cases ran rather than skipping |
| `govulncheck ./...` | pass; 0 vulnerabilities reachable from this code |
| `.scripts/check_pure_go.sh` | pass, 9 invariants hold |
| `.scripts/check_system_owner.sh` | pass, 86 queries compliant |
| `golangci-lint run` | 351 findings, down from **371** on merge base `9b070df`; **zero** in any file this branch touches |
| `go mod tidy` | no change to `go.mod` or `go.sum` |
| `eslint .` | pass |
| `vitest run` | 310/310 across 39 files |
| `tsc -b` | pass |
| `vite build` | pass |
| Docstring coverage on touched files | 350/350 Go functions, 55/55 TypeScript functions |

The lint number is a repository-wide total, not a per-change score. The
remainder (`errcheck` 67, `goconst` 168, `thelper` 39, …) predates this branch
and is spread across packages this change does not touch; reducing it further is
separate work.

## What each closed item means

### G01 — typed GraphQL FileIO and memory

`internal/web/fileio/schema.graphql` publishes seven queries and seven
mutations, regenerated through `make gen` (gqlgen v0.17.94). The resolvers in
`internal/library/fileio` are an adapter, not a second implementation:

- Identity comes from `mcpauth.FromContextOrHeader`, so tenant isolation is
  byte-identical to MCP and the dedicated HTTP routes.
- Typed `files.Error` and `mcpmemory.Error` values map onto GraphQL error
  extensions carrying the same machine-stable `code` and `retryable` fields the
  MCP tool result and the HTTP status mapping expose.
- Every mutation goes through `mcptools.ConditionalFileService`, which the MCP
  tool path now also calls. The precondition gate has exactly one
  implementation, so GraphQL cannot drift into accepting a blind write.
- Per-call plugin routing (`plugin: AUTO | RAG | PAGEINDEX`) threads through
  `mcpplugin.WithOverride`, the same mechanism MCP uses.
- Byte counts and offsets use a new exact `BigInt` scalar, transported as a
  decimal string, because GraphQL's `Int` is 32-bit and JSON numbers lose
  precision above 2^53. History identifiers stay decimal strings.

The former absence tests are replaced with a two-part contract:
`internal/web/fileio_graphql_boundary_test.go` pins the published inventory, the
mandatory version arguments and the exact-arithmetic types, while
`internal/web/fileio_graphql_contract_test.go` executes real GraphQL operations
through the generated schema.

**On ruling out a false positive.** The first version of the contract test
passed even with the shared gate deliberately bypassed, because the test fixture
enforced the precondition itself. The fixture now counts every mutating call
that *reached* it, separating "rejected before storage" from "rejected by
storage". With that counter, bypassing the gate turns the test red, and the
`TestGraphQLAndMCPShareOneConditionalGate` case asserts both interfaces stop
before the backend rather than inside it.

### G03 — fetch output-format selection

`WebFetch(url, output_markdown)` takes the same selection as the MCP tool, and
`WebFetchResult.output_markdown` reports the format that was actually requested
so a client never guesses how to read `content`. The console adds a
Markdown/Raw HTML selector and renders the body as text in both cases — the
remote document is untrusted and is never injected as live markup.

Negative control: hardcoding the resolver back to `true` turns
`TestFetchOutputFormatParityAcrossMCPAndGraphQL/raw_html_requested_explicitly`
red, so the assertion is not vacuous.

### G05 — crawler egress (partial, and honestly so)

The in-repository half is implemented and tested; the renderer is a separate
service that is not in this repository. [The egress
contract](crawler_egress_policy.md) states precisely which half is which.

What is now enforced here: admission publishes the addresses it validated, the
crawl task and the GraphQL worker API carry the pinned host/addresses, the
redirect budget and the subresource decision, and every origin the renderer
reports is re-admitted before any body reaches the caller. A redirect into a
private address, a rebound host, or a chain longer than the budget fails closed.
An unreported chain is classified `ErrEgressUnverified` — distinct from a
violation, because it means nothing was checked — and
`settings.mcp.tools.web_fetch.egress.require_verified` turns it into a failure
once the renderer reports.

What is still not established here: that the renderer pins its sockets, bounds
its redirects, restricts subresources, or reports its chain truthfully. This
item stays partial for that reason.

### G06 — billing-outcome reconciliation

The blocker was that `CheckUserExternalBilling` returned an opaque wrapped
error, so no caller could tell a definitive rejection from an undetermined
remote state. It now returns a classified `*oneapi.BillingError`:

| Outcome | Condition | Charged | Recorded cost | Auto-retry |
| --- | --- | --- | --- | --- |
| `accepted` | HTTP 200 | yes | configured price | n/a |
| `denied` | HTTP 4xx | no | 0 | allowed later, explicitly |
| `unknown` | timeout, transport failure, 5xx | yes | price, flagged indeterminate | **never** |
| `not_attempted` | request never sent | no | 0 | allowed |

The recorded cost now follows the billing outcome, not the tool result. The
previous MCP behavior recorded zero on a provider failure even after a
successful consume, which hid a real charge; that is what made the local log
unusable for reconciliation. An unresolved consume records the price *and*
`indeterminate: true`, so the row is never read as free and never as a receipt.
Both interfaces write this under the reserved `_billing` parameters key, inside
the existing JSONB column, so no migration is needed. GraphQL now also audits a
denial rather than returning before writing anything.

## Defects found and fixed beyond the checklist

Each was reproduced with a failing test first, then fixed; every test is
retained as a regression.

### A permanent GORM prepared-statement deadlock

The full `go test -race -cover ./...` run timed out after 10 minutes in
`internal/web/blog/oneapi`. The cycle is between two resources: GORM holds its
statement-cache mutex across `database/sql.PrepareContext`, which needs a pooled
connection, while a goroutine already inside a transaction owns the only SQLite
connection and needs that same mutex. Neither side can proceed.

It did not reproduce in isolation — it needed the CPU contention of a full
parallel run — so a deterministic reproduction was constructed instead:
`TestSingleConnectionPoolDoesNotDeadlockOnNewStatements` drives 24 workers, half
holding transactions, all preparing uncached statements against a fresh
database. It failed on a 30-second deadline in 3 of 3 runs before the fix and
passes in ~1.1s after it.

Current guidance was checked rather than assumed: `go-gorm/gorm#7350` (open
since January 2025) and `#7465` (open since May 2025) describe exactly this, no
released version fixes it, and disabling `PrepareStmt` is the accepted remedy
for a single-connection pool. The fix therefore ties the statement cache to the
pool size (`minPrepareStmtPoolSize`), which also covers an operator who lowers
`MaxOpenConns` to 1 on PostgreSQL or MySQL, where issue #7465 shows the same
hazard.

### A startup panic for any deployment without telegram configuration

`web.NewResolver` builds a fallback telegram controller whenever `cmd/api.go`
could not create the telegram service and the `telegram` task was not requested.
That constructor called `log.Logger.Panic` on an unusable throttle
configuration, so a deployment that never configured telegram died at startup —
taking GraphQL, MCP and the dedicated HTTP routes with it, and contradicting the
documented contract of logging an error and continuing to serve.

The throttle now degrades and logs, `TelegramMonitorAlert` refuses to send
rather than dereferencing a nil limiter (it does not push unthrottled alerts),
and `cmd/api.go` gained `ValidateTelegramThrottleConfig` so an explicitly
requested `telegram` task still fails fast. Three of the four new tests fail
against the original implementation with the same `create telegramThrottle`
panic the contract test first hit.

### Stale tests that encoded superseded behavior

- `TestSanitizeURLForLog` still asserted that log fields keep the URL path and
  echo malformed input verbatim — exactly what the R01 redaction fix removed.
- Two browser API tests asserted `/tools/...` while the server mounts
  `<prefix>/tools/...` whenever a public prefix is configured.
- `home.test.tsx` asserted the hardcoded tariff that P01 replaced with runtime
  pricing metadata.
- `TestRecordToolInvocationCostTracking` encoded the cost/tool-result coupling
  that the billing-outcome contract replaces.

Each now pins the shipped contract, and the pricing test additionally asserts
that a charged operation is never rendered as free when metadata is absent.

### Dead code and lint regressions left by earlier refactors

Six functions became unreachable when their call sites moved to `toolpolicy`
(`resolveOutputMarkdownArg`, `parseExplicitFalseBool`, `validateFetchURL`,
`findTopK`, `toInt`, `cloneArguments`), five `nilerr` returns in the new history
tool carried no justification, two `gmw.GetLogger` nil checks were dead because
that function never returns nil, `copy` shadowed the builtin, and two frontend
files embedded control characters in regular-expression literals, which eslint
rejects. The permissive `output_markdown` coercion the dead helpers implemented
contradicted this PR's own item I05, so their tests were re-pointed at the
strict decoder the tool actually uses rather than deleted.

## What is still not claimed

- The external renderer's own egress enforcement (see G05 above).
- Reconciling an `unknown` billing row against the billing service. That is an
  operational procedure; nothing here issues a refund or re-posts a consume.
- Per-user authorization and live backend health. The runtime `interfaces`
  catalog describes configured adapters only, by design.
- A production deployment. Nothing here was merged or deployed.
- The 351 remaining repository-wide `golangci-lint` findings outside the files
  this branch touches.
