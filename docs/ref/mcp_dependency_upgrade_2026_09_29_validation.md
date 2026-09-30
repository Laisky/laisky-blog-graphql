# MCP dependency upgrade validation — 2026-09-29

## Tested revisions

- Original repository baseline: `c31b0bec36935bea60f7553adc428d914e61c327`.
- Initial dependency implementation: `a55ae533788bb6d37b4c274e858426937afe2b43`;
  initial cleanup: `02e9e3850f385f12c93ce33b1b176180467f79f8`.
- Recovered session-ownership implementation: `0393d0920a78fabdd19137ecaf243f9c5f1d2cd7`.
  This includes the later application security fixes; the initial dependency-only
  acceptance must not be mistaken for verification of those changes.
- Recovery closure removes the temporary review-audit workflow, promotes the
  20-repeat session test into the retained PR CI, and aligns these documents.
  It leaves the recovered runtime code, dependencies and generated files unchanged.
- Pull request: [#51](https://github.com/Laisky/laisky-blog-graphql/pull/51).
  Exact final-head CI links are recorded in its description and discussion.

## Recovered implementation: behavior, integration and build evidence

[Dependency compatibility run for 0393d09](https://github.com/Laisky/laisky-blog-graphql/actions/runs/36610916329):

| Check | Result |
| --- | --- |
| Module download, verification, tidy and clean lockfiles | Pass |
| MCP raw-wire acceptance with race detector | Pass |
| Full `go test -race -cover -timeout 5m ./...` | Pass |
| All-command build | Pass |
| All-package `go vet` | Pass |
| Pure-Go and system-owner isolation invariants | Pass |
| `make gen`, tidy and clean generated/module files | Pass |

[Application checks for 0393d09](https://github.com/Laisky/laisky-blog-graphql/actions/runs/36610916490):
real PostgreSQL FileIO/auth race tests and vet passed; frontend behavior tests,
lint and production build passed.

[Repeated session acceptance and controlled lint audit](https://github.com/Laisky/laisky-blog-graphql/actions/runs/36610910407):
`go test -race -count=20 -timeout 5m ./internal/mcp -run TestMCPReview` passed.
The same command is retained as a named, failing-on-error PR CI step rather than
being lost when the temporary audit is removed. Tests cover owner/other/missing/
malformed credentials, three legacy revisions, tampered IDs, canonical query/header
forms, modern stateless isolation, concurrency, and valid owner GET/DELETE across
handler instances. See [session ownership](pr51_session_isolation_2026_09_29.md).

The PDF fixture is rendered in memory using valid pdfcpu JSON, rather than
reusing a stale disk file. A test checks exactly three pages and expected text
with both parser selections. Fixture generation failures now fail the tests.
Cancellation and deadline errors propagate through the context-aware pdfcpu APIs.

## Full lint audit: existing debt, not a clean pass

The [original baseline/upgrade audit](https://github.com/Laisky/laisky-blog-graphql/actions/runs/36596433310)
and the [recovered review audit](https://github.com/Laisky/laisky-blog-graphql/actions/runs/36610910407)
used Go `1.27.1`, goimports `v0.50.0`, and golangci-lint `v2.14.0` on both sides.
The recovered audit compares the pre-review PR head `02e9e38` with `0393d09`.

| Metric | Original baseline | Dependency upgrade | Session-ownership fix |
| --- | ---: | ---: | ---: |
| `make lint` findings | 353 | 353 | 353 |
| Newly introduced normalized findings | — | 0 | 0 |
| Removed normalized findings | — | 0 | 0 |

The comparison uses a multiset of `(repository-relative file, diagnostic message
including linter name)`, ignoring line/column offsets that move when code is
inserted. Serialize its sorted entries as compact JSON, with separators `(',', ':')`.
All sorted lists have SHA-256:

```text
b6e1975cdbf2055b063cb14240b08b6690fcb5896fa737074b29bf47bc0778ce
```

Recovered artifact IDs are `11052964255` (baseline) and `11053118920` (review).
The formatting-only diffs also match byte-for-byte: both change import grouping
in the generated GraphQL file. They were not hand-applied to generated output;
clean regeneration remains a separate passing check.

This is **not** a passing `make lint` result. Both audit jobs remain failed to
preserve that fact. No lint rule, warning or required repository check was
suppressed to make the update appear green. Broad cleanup of the existing 353
findings is separate from this dependency update.

### Historical vulnerability scan

The initial dependency audit separately ran `govulncheck ./...` after lint had
failed, finding zero reachable vulnerabilities on the initial upgrade. That scan
was not rerun by the later session-review audit and is not presented as a fresh
scan of its source changes. The upgraded graph had findings in two imported
packages and one required module not identified as called by the program. Zero
reachable findings is not a claim that every dependency is vulnerability-free
or that static analysis proves safety.

## Red-to-green evidence

1. [Initial broad upgrade](https://github.com/Laisky/laisky-blog-graphql/actions/runs/36593338609)
   failed compilation on pdfcpu's new context parameters and pgxmock/v4's missing
   implementation of pgx 5.11 `Rows.TypeMap`.
2. [API compatibility repair](https://github.com/Laisky/laisky-blog-graphql/actions/runs/36594892005)
   passed MCP acceptance/build/vet and exposed an invalid PDF fixture after
   replacing `t.Skipf` with a hard failure. Repairing the fixture then made the
   complete test suite pass.
3. [Tests-only session reproduction](https://github.com/Laisky/laisky-blog-graphql/actions/runs/36607704142)
   at `8b2c748` exposed request-identity inheritance and missing legacy ownership
   enforcement. A synthetic leaf counter demonstrated dispatch before rejection.
   This is not evidence that every production tool's authorization was bypassed,
   nor that the dependency upgrade introduced the preexisting boundary problem.
4. The recovered implementation removes identity fallback, enforces credential-bound
   sessions on POST/GET/DELETE, and passes the full and 20-repeat race suites.
   The HMAC uses the request API key directly; the preliminary unkeyed hash flagged
   in review was removed, without suppressing the scanner rule.

## Execution and retained scope

The editing container could not download modules. Temporary isolated Actions
jobs resolved dependencies, regenerated GraphQL code and produced evidence.
Only a separate allowlisted publisher had write permission, and it pushed only
the named feature branch without force. The temporary updater, patches and both
audit workflows are removed from the final PR diff. The retained compatibility
workflow is read-only, runs both protocol and repeated session acceptance, and
does not auto-update dependencies or suppress the known lint findings.

All interrupted commits were retained on `codex/mcp-sdk-20260929`; no replacement
PR or force push was used. No deployment, merge or production schema migration
was performed. Authenticated legacy clients require a fresh session and the same
API key on every request after rollout. See [protocol compatibility](mcp_protocol_2026_07_28.md)
and [session ownership](pr51_session_isolation_2026_09_29.md) for the feature boundary,
security assumptions, client migration and regression matrices.
