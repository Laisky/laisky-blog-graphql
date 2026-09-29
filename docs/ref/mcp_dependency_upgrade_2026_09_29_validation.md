# MCP dependency upgrade validation — 2026-09-29

## Tested revisions

- Baseline: `c31b0bec36935bea60f7553adc428d914e61c327`.
- Accepted implementation: `a55ae533788bb6d37b4c274e858426937afe2b43`.
- Subsequent cleanup removes the temporary audit workflow and adds this report;
  it does not change the tested Go code, modules, generator, frontend or Dockerfile.
- Pull request: [#51](https://github.com/Laisky/laisky-blog-graphql/pull/51).

## Behavior, integration and build evidence

[Dependency compatibility run](https://github.com/Laisky/laisky-blog-graphql/actions/runs/36596504134):

| Check | Result |
| --- | --- |
| Module download, verification, tidy and clean lockfiles | Pass |
| MCP raw-wire acceptance with race detector | Pass |
| Full `go test -race -cover -timeout 5m ./...` | Pass |
| All-command build | Pass |
| All-package `go vet` | Pass |
| Pure-Go and system-owner isolation invariants | Pass |
| `make gen`, tidy and clean generated/module files | Pass |

[Existing application checks](https://github.com/Laisky/laisky-blog-graphql/actions/runs/36596503851):
real PostgreSQL FileIO/auth race tests and vet passed; frontend behavior tests,
lint and production build passed. No production credentials or paid model
calls were needed by these checks.

The new PDF fixture is rendered in memory using valid pdfcpu JSON, rather than
reusing a stale disk file. A test checks exactly three pages and expected text
with both parser selections. Fixture generation failures now fail the tests.
Cancellation and deadline errors propagate through the context-aware pdfcpu APIs.

## Full lint audit: existing debt, not a clean pass

[Controlled baseline/upgrade audit](https://github.com/Laisky/laisky-blog-graphql/actions/runs/36596433310)
used Go `1.27.1`, goimports `v0.50.0`, and golangci-lint `v2.14.0` on both revisions.

| Metric | Baseline | Upgrade |
| --- | ---: | ---: |
| `make lint` findings | 353 | 353 |
| Newly introduced normalized findings | — | 0 |
| Removed normalized findings | — | 0 |
| Reachable vulnerabilities reported by `govulncheck ./...` | 0 | 0 |

The lint comparison strips ANSI escapes and compares a multiset of
`(repository-relative file, diagnostic message including linter name)`, ignoring
line/column offsets that move when code is inserted. Both sorted JSON lists
have SHA-256:

```text
b6e1975cdbf2055b063cb14240b08b6690fcb5896fa737074b29bf47bc0778ce
```

This is **not** a passing `make lint` result. Both audit jobs remain failed to
preserve that fact. No lint rule, warning or required repository check was
suppressed to make the update appear green. Broad cleanup of the existing 353
findings is separate from this dependency update.

The vulnerability scan was run independently after the lint failure, so it
was not skipped by Make's early exit. The upgraded graph still has findings
in two imported packages and one required module that the scanner did not
identify as called by this program. A zero reachable count is not a claim that
every dependency is vulnerability-free or that static analysis proves safety.

## Red-to-green evidence

1. [Initial broad upgrade](https://github.com/Laisky/laisky-blog-graphql/actions/runs/36593338609)
   failed compilation on pdfcpu's new context parameters and pgxmock/v4's missing
   implementation of pgx 5.11 `Rows.TypeMap`.
2. [API compatibility repair](https://github.com/Laisky/laisky-blog-graphql/actions/runs/36594892005)
   passed MCP acceptance/build/vet and exposed an invalid PDF fixture after
   replacing `t.Skipf` with a hard failure.
3. The accepted implementation corrects the fixture's invalid header and
   position field, then passes the complete test suite rather than skipping PDF tests.

## Execution and retained scope

The editing container could not download modules. Temporary isolated Actions
jobs resolved dependencies, regenerated GraphQL code and produced evidence.
Only a separate allowlisted publisher had write permission, and it pushed only
the named feature branch without force. Those temporary workflows and their
patch are removed from the final PR diff. The retained PR compatibility workflow
is read-only and performs validation; it does not auto-update dependencies.

No deployment, merge, production schema migration or authentication relaxation
was performed. See [protocol compatibility](mcp_protocol_2026_07_28.md) for the
implemented feature boundary, upstream sources and test matrix.
