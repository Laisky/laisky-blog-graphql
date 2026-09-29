# PR #51: project-wide quality closure — 2026-09-29

## Scope and accepted source

This report supersedes the earlier PR #51 decision to leave the 353 baseline
lint findings as existing debt. Those findings are now fixed, not grandfathered.
The earlier dependency and session reports remain historical evidence; they are
not the current quality verdict.

- Initial project-wide runtime repair commit: `9c642e683f9b99d1b98eacbcb707ac6a3ea667ab`.
- Initial project-wide source tree: `9ee695d5fedf9d0c3785e3c3a7be056c9973acbe`.
- [Candidate acceptance](https://github.com/Laisky/laisky-blog-graphql/actions/runs/36638208283/job/109643948422)
  passed full race/coverage tests, 20 session-test repetitions, full `make lint`,
  uncapped structured lint, build, imported-package vulnerability scanning, module
  verification, generation and a check that acceptance did not modify the source.
- Artifact `11065538382` (`quality-final-acceptance`) preserves the exact patch,
  tree, allowlist, test results, lint JSON and vulnerability report. ZIP SHA-256:
  `48ba1d9b106fef6ba4ad5c599729af0893968e2acff8a2edc03e3f9cc8d10d72`.
- The candidate's automatic publisher lacked workflow-write permission. Its push
  was rejected; the already-tested tree was committed through the owner's
  authorized GitHub connection instead. No permissions or protections were weakened.
- The closing commit adds this report and a regression test for the non-mutating
  formatting gate. Final-head PR check links are recorded in the PR discussion.

## Lint inventory: 353 to zero

The [recovered uncapped baseline](https://github.com/Laisky/laisky-blog-graphql/actions/runs/36631569601)
contains 353 findings across 22 linters. The accepted candidate contains **zero**.
`.golangci.lint.yml` is unchanged. Comparing suppression comments with the recovered
source finds no new `nolint` comments; four obsolete comments were removed.

| Category | Before | Accepted candidate |
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
`wastedassign`. Existing linter configuration/deprecation notices are not hidden;
zero diagnostics is not a claim that the linter emits no informational notices.

## Verified behavioral repairs

| Boundary | Repair and retained regression evidence |
| --- | --- |
| Frontend filesystem boundary | Open assets inside the configured root and serve the same opened handle. The cached `index.html` path uses that boundary too. `TestSPARootBoundary` and `TestSPAIndexRootBoundary` reject outward symlinks and retain legitimate internal links. |
| Provider credentials in logs/errors | Remove complete API-key-bearing URLs and upstream bodies from diagnostics; redact raw/escaped keys in returned provider errors while preserving cancellation identity. `TestSearchNeverExposesProviderKey` checks successful, HTTP-error and transport-error paths. |
| Multipart resource limits | Enforce total body, per-image and file-count bounds before large reads; reject overflowed limit calculations, malformed parts and canceled requests. Read/close/temporary-file cleanup errors are no longer silently lost. |
| Attachment index parsing | Reject malformed, negative and overflowing indexes instead of using an unsupported scanner format. Remove the obsolete scanner helper. |
| Concurrent shadow-plugin shutdown | Preserve caller context values while detaching request cancellation; use an independent bounded operation lifecycle. Synchronize admission and draining, make concurrent stops safe, and do not close a recorder still in use. `TestShadowPreservesValuesAndNeverClosesActiveRecorder` and `TestShadowConcurrentAdmissionAndStop` exercise those boundaries. |
| Avro compatibility and malformed input | Retain existing Arweave tag bytes with a maintained decoder and bounded allocation. Check the reader's terminal error and trailing bytes; truncated data must not become a successful decode. Tests use independent goar/goavro compatibility controls and malformed-length/trailing-data cases. |
| Request caching | Propagate JSON/hash errors, reject invalid numeric values before cache/network use, and include the configured model in cache identity. |
| Benchmark evidence | Use decimal evidence indexes beyond nine, preserve writer/close failures, and reject invalid/ambiguous golden-version input. Advance the reader-prompt version for the changed evidence format. |
| Context and error propagation | Forward service/indexer contexts, bound Git subprocess execution, close SQL rows before subsequent work, preserve primary failures when cleanup also fails, and retain the existing HTTP status contract for search failures. |
| Maintainability | Extract cohesive helpers from complex rename/search/evaluation functions, keep wire JSON field names stable, deduplicate constants, and fix deprecated/dead code without changing the tenant predicates or weakening assertions. |

The earlier raw-wire MCP and owner-bound legacy-session regressions remain in
place, including negative side-effect assertions and 20 race-enabled repetitions.
The static-file and Avro defects were reproduced in the recovered baseline; the
cached-index escape and credential logging were separately tested red before
repair. Ordinary positive-control cases remain in the same suites.

## Follow-up: MongoDB query alerts

The post-cleanup CodeQL run reported 15 query-taint alerts in blog comments,
post/user lookup, verification-code deletion and Twitter lookup. They are not
counted as 15 confirmed injection vulnerabilities: the affected values are
already strings or ObjectIDs under fixed BSON keys, and the driver does not
parse those string values as JSON query operators.

The follow-up first tested the unchanged production queries against MongoDB 8.0.
The tests-only revision `1faa6a820388ce88e476d90d2503849dfc0b1dfd` passed the
[real MongoDB contract job](https://github.com/Laisky/laisky-blog-graphql/actions/runs/36646209044).
Operator-shaped strings did not select the canary record, and positive lookups
still returned it. This rules out the alleged injection for the exercised paths;
it is not a blanket claim about every possible database query.

The query representation is then narrowed to fixed, local BSON struct types.
Each type accepts only the intended scalar values; callers cannot add operator
keys. Fields have no `omitempty`, so empty input never turns an identity filter
into an unrestricted empty filter. ObjectIDs stay ObjectIDs and Twitter IDs
remain strings, including leading zeros. Existing normalization, authorization,
pagination and update documents are unchanged. No CodeQL exclusions, taint
barriers or warning suppressions were introduced.

Retained tests compare the real driver's BSON serialization to independently
specified pre-refactor filters, including empty, Unicode, null-byte and
operator-shaped values. The dedicated `mongo-query-contract` PR job requires a
working disposable MongoDB instance and repeats the race-enabled tests three
times. It exercises production lookups/comment/category boundaries and verifies
that an account-and-purpose deletion retains both other accounts and other
purposes. Local runs without the explicit database URI may skip the integration
test; CI always supplies it, and a connection failure fails the job.

The [controlled comparison](https://github.com/Laisky/laisky-blog-graphql/actions/runs/36646206339)
preserves pre-refactor and candidate logs plus the exact source patch. Final-head
CodeQL and all four PR workflow results are recorded in the PR discussion. The
temporary source-preparation workflow is removed from the delivered tree; only
the read-only MongoDB acceptance job remains.

## Dependencies and security interpretation

The MCP SDK remains `mcp-go v1.1.1`, with the earlier pdfcpu context, pgx/pgxmock,
and module-selected gqlgen migrations retained. Frontend packages and transitive
pins are now included in the quality pass, with a frozen-lockfile installation
and an audit covering development dependencies and all reported severities.

The legacy `hamba/avro` import used by goar is replaced by a minimal compatibility
adapter over `github.com/iskorotkov/avro/v2 v2.34.0`. The adapter preserves the
existing count-only block encoding and caps decoder allocations; it is not a
renamed copy of the vulnerable decoder.

The gRPC pin is `v1.85.0-dev.0.20260825072537-93e31b48545e`, the patched revision
identified by [GO-2026-6443](https://pkg.go.dev/vuln/GO-2026-6443). This is an
explicit development/pseudo-version, **not a stable-release claim**. Keep its
compatibility tests and vulnerability gates when replacing it with a later release.

Both reachable-symbol and imported-package vulnerability checks pass. One
module-level advisory remains: [GO-2026-5932](https://pkg.go.dev/vuln/GO-2026-5932)
concerns the unmaintained `golang.org/x/crypto/openpgp` package and has no fixed
release. The module is needed by other cryptographic packages, but the affected
OpenPGP package is not in this application's import graph. The package-level
check is deliberately retained so introducing that import fails acceptance.
This is a classified non-applicable package exposure, not a suppressed warning
or a statement that every required module is vulnerability-free.

## Permanent acceptance gates

`make lint` is now read-only: it checks formatting, uses `go mod tidy -diff`,
runs vet, uncapped lint, vulnerability checks and both repository invariants.
`make format-go` is the separate, explicit source-rewriting command. `make test`
runs the real race-enabled Go suite instead of the obsolete tox invocation.

The quality workflow captures the reviewed commit and runs source-dependent
scans before `make lint`. It always preserves `format.patch` and fails if checks
modify tracked files or create untracked source. The formatting-gate regression
creates dirty and clean temporary fixtures, checks rejection/acceptance, and
compares bytes to prove the gate does not silently repair what it is checking.
GraphQL generation is a separate reproducibility check; generated files are
produced by the module-selected generator, never edited by hand.

```sh
# After intentional source changes:
make format-go
make gen

# Acceptance (should leave the checkout unchanged):
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

The separate application workflow runs FileIO/auth tests with real PostgreSQL.
A passing command does not imply 100% coverage or completion of every future
proposal/conformance scenario. This closure covers the inventoried diagnostics,
reproduced production defects and the checked dependency graph; it does not
prove the absence of unknown defects.

No merge, deployment, production data migration or paid model call is part of
this acceptance. Authenticated legacy MCP clients still require a fresh session
and the same API key on each request after rollout; see
[session ownership and rollout](pr51_session_isolation_2026_09_29.md).
