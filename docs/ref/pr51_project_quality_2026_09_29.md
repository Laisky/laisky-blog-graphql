# PR #51: project-wide quality closure

Last verified: 2026-09-30 UTC.

## Scope and accepted source

This report supersedes the earlier decision to leave 353 baseline lint findings
as existing debt. Those findings are fixed, not grandfathered. Earlier dependency
and session reports remain historical evidence, not the current quality verdict.
All interrupted work is retained on PR #51, without a replacement PR or force push.

Accepted runtime: `eff47d5e1cc1db5cb664cb789ddeba738b4b840c`.
Subsequent cleanup removes temporary evidence collection and aligns documentation;
it does not replace the verified runtime fixes. Final cleanup-head checks are
recorded in the PR discussion after they complete.

## Current acceptance

| Gate | Result and evidence |
| --- | --- |
| Full read-only `make lint`, uncapped diagnostics and formatting/mutation checks | **Pass**, zero issues and empty `format.patch`: [project quality](https://github.com/Laisky/laisky-blog-graphql/actions/runs/36650150766) |
| Reachable-symbol and imported-package vulnerability scans | **Pass**, zero affected findings at both levels: same quality run |
| Frontend lint, tests, production build and all-severity/development-dependency audit | **Pass**: same quality run |
| Module locks, MCP wire protocol, 20 race-enabled session repetitions, full Go race/coverage, build/vet and clean generation | **Pass**: [compatibility](https://github.com/Laisky/laisky-blog-graphql/actions/runs/36650150642) |
| Pure-Go and system-owner SQL invariants | **Pass**: compatibility and quality runs |
| Real PostgreSQL FileIO/auth and existing frontend acceptance | **Pass**: [application checks](https://github.com/Laisky/laisky-blog-graphql/actions/runs/36650150756) |
| Real MongoDB 8 query-contract canaries, three race-enabled repetitions | **Pass**: [MongoDB contracts](https://github.com/Laisky/laisky-blog-graphql/actions/runs/36650150696) |
| SSRF, committed response MIME types, log privacy and body replay, three race-enabled repetitions | **Pass**: [network/security regressions](https://github.com/Laisky/laisky-blog-graphql/actions/runs/36650150958) |
| Complete CodeQL results, not only changed-line annotations | **Zero results** for Go, JavaScript/TypeScript and Actions; both PR head/merge open-alert queries are empty |

The quality archive records GitHub's synthetic PR merge
`1bc4faf2f062b90c6180871e2ebb1ff438406166`, containing the accepted head and the
unchanged master base `c31b0bec36935bea60f7553adc428d914e61c327`. Source-dependent
checks inspect that reviewed checkout and fail on any source mutation. Artifact
`11070141922` has ZIP SHA-256
`e82e9348a245356b5f1013dfc18626b6dd7ad8858705229510bdc36069bc7fae`.

[Complete CodeQL evidence](https://github.com/Laisky/laisky-blog-graphql/actions/runs/36650598166)
contains exact-head analyses `1863382483` (Go), `1863374525` (JavaScript/TypeScript)
and `1863373765` (Actions), all with no analysis errors and zero SARIF results.
Artifact `11070301907` has ZIP SHA-256
`1d224953ef5461b0e6cddb59d0b40e654b2d026b764cd3b6706a3566e223fe82`.
The temporary read-only collector is removed. Default-branch alerts may remain
until merge/rescan; no alerts were dismissed to obtain the PR result.

## Lint inventory: 353 to zero

The [recovered uncapped baseline](https://github.com/Laisky/laisky-blog-graphql/actions/runs/36631569601)
contains 353 findings across 22 linters. The accepted runtime contains **zero**.
`.golangci.lint.yml` is unchanged; no new suppression comments were introduced.

| Category | Before | Accepted runtime |
| --- | ---: | ---: |
| Duplicate constants (`goconst`) | 163 | 0 |
| Unchecked errors (`errcheck`) | 67 | 0 |
| Test helper attribution (`thelper`) | 39 | 0 |
| Static correctness (`staticcheck`) | 18 | 0 |
| JSON error handling (`errchkjson`) | 9 | 0 |
| Line length and spelling | 16 | 0 |
| Context propagation and request contexts | 7 | 0 |
| Security diagnostics (`gosec`) | 6 | 0 |
| Remaining 12 categories | 28 | 0 |
| **Total** | **353** | **0** |

Remaining categories are `err113`, `exhaustive`, `gocognit`, `mirror`, `musttag`,
`nilerr`, `nilnil`, `predeclared`, `sqlclosecheck`, `unconvert`, `unused` and
`wastedassign`. Existing configuration/deprecation notices are not hidden;
zero code diagnostics does not mean the linter emits no informational notices.

The initial project-wide repair was accepted before publication in
[run 36638208283](https://github.com/Laisky/laisky-blog-graphql/actions/runs/36638208283/job/109643948422).
Artifact `11065538382` preserves its exact patch and evidence, ZIP SHA-256
`48ba1d9b106fef6ba4ad5c599729af0893968e2acff8a2edc03e3f9cc8d10d72`.
The candidate publisher lacked workflow-write permission; its rejected push was
not bypassed. The tested tree was committed through the owner's authorized
GitHub connection instead, without weakening permissions or protections.

## Verified behavioral repairs

| Boundary | Repair and retained regression evidence |
| --- | --- |
| Static frontend files | Open assets and cached index files inside the configured root and serve the same opened handle. `TestSPARootBoundary` and `TestSPAIndexRootBoundary` reject outward symlinks and preserve legitimate internal links. |
| Provider diagnostics | Remove API-key-bearing URLs and upstream bodies; redact raw/escaped keys from returned errors while preserving cancellation identity. `TestSearchNeverExposesProviderKey` covers success, HTTP failure and transport failure. |
| Multipart limits and attachment parsing | Bound total body, per-image and file-count allocations; reject overflow, malformed parts and invalid indexes; preserve cancellation and read/close/temporary-file cleanup failures. |
| Shadow-plugin shutdown | Preserve caller context values with a bounded independent lifecycle, synchronize admission/draining and concurrent stops, and never close an active recorder. Dedicated concurrent/race tests exercise both boundaries. |
| Avro compatibility | Use a maintained decoder with bounded allocation and independent goar/goavro wire controls. Truncation, terminal read errors and trailing bytes must fail rather than decode successfully. |
| Cache and benchmark correctness | Include the model in cache identity; reject invalid numbers; propagate JSON/hash/write failures. Evidence indexes beyond nine remain decimal and golden-version input is validated. |
| Context/error handling | Forward service/indexer contexts, bound Git subprocess execution, close SQL rows before subsequent work, preserve primary errors when cleanup also fails, and retain the existing search HTTP status contract. |
| MCP identity and sessions | Remove session-to-identity fallback. Current credentials alone select preferences; credential-bound legacy proofs enforce POST/GET/DELETE ownership with negative side-effect and cross-instance positive controls. |
| Image network boundary | Dial a validated numeric public address, preserve logical Host and TLS identity, reject disallowed DNS answers/redirects/address classes, disable environment proxies, and enforce a single deadline and bounded response reading. |
| MCP response context | Enforce JSON/SSE or inert plain-text MIME types and `nosniff` at the buffered/streamed write boundary. Real HTTP tests cover committed headers and early SSE flushes without HTML-escaping wire bytes. |
| MCP HTTP logging | Redact query credentials and URL userinfo without changing the request. Inspect at most 4097 bytes for a 4096-byte log prefix; replay all downstream bytes, terminal read errors and original Close behavior. This is not a new global MCP request-size contract. |
| Maintainability | Extract cohesive helpers, preserve wire field names and tenant predicates, deduplicate constants, and remove dead/deprecated code without weakening assertions. |

The static-file, Avro, cached-index and provider-logging defects were reproduced
before their fixes. The tests-only HTTP logging commit `dae48053c19f180ee1e9907e82091c8583fb9bfc`
failed [run 36649626987](https://github.com/Laisky/laisky-blog-graphql/actions/runs/36649626987)
on credential leakage, premature Close and lost partial-read data. Other protocol,
session, build/vet and generation checks passed. The same tests pass after
`eff47d5`, including three race-enabled repetitions. See
[HTTP security acceptance](pr51_http_security_2026_09_30.md) for the complete
negative/positive-control matrix and scanner interpretation.

## MongoDB query alerts: validation before refactoring

CodeQL reported 15 query-taint alerts in blog comments, post/user lookup,
verification deletion and Twitter lookup. These are **not** counted as 15 confirmed
injection vulnerabilities: the original values were already strings or ObjectIDs
under fixed BSON keys, not JSON parsed into query operators.

The tests-only revision `1faa6a820388ce88e476d90d2503849dfc0b1dfd` first passed the
[real MongoDB contract job](https://github.com/Laisky/laisky-blog-graphql/actions/runs/36646209044)
against unchanged production queries. Operator-shaped strings could not select
the canary record; positive lookups still returned it. The
[controlled before/after comparison](https://github.com/Laisky/laisky-blog-graphql/actions/runs/36646206339)
retains both logs and the exact refactor patch.

Queries now use fixed local BSON struct types with scalar fields and no
`omitempty`: callers cannot introduce operator keys, and empty input never turns
an identity filter into an unrestricted empty filter. ObjectIDs stay ObjectIDs;
Twitter IDs stay strings, including leading zeros. Normalization, authorization,
pagination and update documents are unchanged. No CodeQL exclusions or custom
taint barriers were added.

Tests compare real BSON serialization to independent expected pre-refactor
filters, including empty, Unicode, null-byte and operator-shaped values. Real
MongoDB tests exercise production lookup/comment/category paths and prove
account-and-purpose deletion preserves other accounts and purposes. Local runs
without the explicit database URI may skip integration tests; the retained PR
job always supplies MongoDB and fails on connection or contract failure.

## Dependencies and remaining advisory classification

The MCP SDK is `mcp-go v1.1.1`, retaining pdfcpu context support, the pgx/pgxmock
migration and module-selected gqlgen generation. Frontend and transitive updates
are locked and audited across all severities, including development dependencies.

The legacy `hamba/avro` import used by goar is replaced by a minimal compatibility
adapter over `github.com/iskorotkov/avro/v2 v2.34.0`. The adapter preserves the
existing count-only block encoding and caps decoder allocations; it is not a
renamed copy of the vulnerable decoder.

The gRPC pin is `v1.85.0-dev.0.20260825072537-93e31b48545e`, the patched revision
identified by [GO-2026-6443](https://pkg.go.dev/vuln/GO-2026-6443). It is explicitly
a development/pseudo-version, **not a stable release**. Retain compatibility and
vulnerability gates when replacing it with a later release.

One module-level advisory remains: [GO-2026-5932](https://pkg.go.dev/vuln/GO-2026-5932)
concerns unmaintained `golang.org/x/crypto/openpgp`, with no fixed release. Other
cryptographic packages require the module, but this application does not import
the affected OpenPGP package. Both symbol-reachable and imported-package scans
report zero affected findings. The package-level gate will reject introducing
that import. This is classified non-applicable package exposure, not a suppressed
warning or a claim that every required module is advisory-free.

## Permanent acceptance and rollout

`make lint` is read-only: formatting, `go mod tidy -diff`, vet, uncapped lint,
vulnerability checks and both repository invariants. `make format-go` explicitly
rewrites formatting; `make test` runs the actual race-enabled Go suite rather than
the obsolete tox command. The quality workflow scans the archived checkout,
retains `format.patch`, and fails on tracked/untracked source mutation. A regression
proves dirty input is rejected and clean input accepted without rewriting either.
GraphQL generation is separately checked for reproducibility.

```sh
make format-go
make gen
make lint
make test
go build ./...
go test -race -count=20 -timeout 5m ./internal/mcp -run TestMCPReview
govulncheck -scan package ./...
cd web
pnpm install --frozen-lockfile
pnpm lint && pnpm test && pnpm build
pnpm audit
```

The five retained PR workflows cover quality, compatibility, PostgreSQL/frontend,
MongoDB and network security; default CodeQL covers Go, JavaScript/TypeScript and
Actions. All temporary updater/collector workflows are removed after collection.
Passing these finite tests/scans does not establish 100% coverage, every future
proposal's acceptance, or absence of unknown defects.

No merge, deployment, production data migration or paid model call is part of
acceptance. Authenticated legacy MCP clients require a fresh session and the same
API key on every request after rollout. See
[session ownership and rollout](pr51_session_isolation_2026_09_29.md).
