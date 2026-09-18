# PR #49: behavioral review regressions

Date: 2026-09-18. Reviewed parent: `0a8975a6726f122fb15f0975800a7d8391a871e7`.
This follow-up preserves that commit's GraphQL, billing, egress and acceptance
work. The older PR description still named `66f5c82`; the branch ref, not that
stale prose, was used as the editing baseline.

## Findings and disposition

| Review comment | Confirmed behavior before the fix | Change and retained regression |
| --- | --- | --- |
| 4044307237 | `MemoryAfterTurn` accepts negative boundaries, returns `ok: true`, and passes zeroed boundaries to a persistence spy that deliberately performs no validation. | `afterTurnRequest` rejects either negative value before conversion and returns an error; the public resolver propagates it before calling persistence. Nil, zero and positive inputs retain their previous behavior. `memory_after_turn_review_test.go` exercises the same stable resolver method before and after the signature change. |
| 4044307249 | Address-capture lookup number two and subsequent chain lookups receive a context without an application deadline. Caller cancellation is also converted to a generic DNS error. | Give each address lookup a ten-second child deadline, preserve earlier caller deadlines/cancellation, distinguish a resolver timeout, release the child on return and retain redaction of resolver details. `egress_dns_review_test.go` checks the contexts actually passed to the injected DNS function, including both lookups and both reported hops. |
| 4044307255 | An empty or unknown typed `BillingError.Outcome` passes through classification; the result reports `Charged() == false` and is not indeterminate. | Whitelist the four defined outcomes. Invalid typed values become `BillingUnknown`, possibly charged, indeterminate and unsafe to retry. `outcome_review_test.go` covers direct/wrapped invalid values, all defined states, nil errors, typed-nil errors and unclassified errors. |
| 4044307265 | With a fresh configuration and no override, a missing/empty renderer chain is accepted by the result-verification gate. An explicit true override already rejects it. | Default `RequireVerified` to true when its configuration key is absent. Missing evidence fails by default. An explicit false remains a documented unsafe compatibility choice; reported violations always fail. `egress_default_review_test.go` distinguishes absence from explicit false and includes valid/invalid chain controls. |

The billing finding is **defensive boundary hardening**, not evidence of a live
incorrect charge or a production producer emitting an invalid enum. The current
consume implementation constructs defined states. No consume, refund, retry or
pricing behavior is changed for those states.

The renderer finding is a **secure-default policy change**. Earlier code and its
manual explicitly allowed missing evidence by default; this change intentionally
replaces that permissive default, rather than pretending the old default was
undocumented. See [the updated rollout contract](../manual/crawler_egress_policy.md).

## Reproduction and actual execution

The regression assertions were written before modifying the subject functions.
The original production files were retrieved at the exact parent and checked
against their Git blob identities:

| Subject file | Parent blob |
| --- | --- |
| `internal/library/fileio/memory_input.go` | `be3cda2d8ebfc1076ed6e6687c73f347a38305ad` |
| `internal/library/fileio/mutation.go` | `fb4e487f398c97636519d47715a8f33f12cca282` |
| `internal/library/toolpolicy/egress.go` | `6075c03e6af331c1fb00a38b96722b26e28f57ce` |
| `internal/library/toolpolicy/input.go` (unchanged) | `27632b4cadedb34d6b94fc9d9a4e2a2ad4dedb51` |
| `library/billing/oneapi/outcome.go` | `bf989a9915aaf5a68f9cc51713f3608d26d2c66c` |
| `library/search/egress.go` | `b07aa215e1f2b81ae232c8ea353e153732b1b1c1` |

The same committed regression assertions produced the following leaf-case results
in the local isolated test closures. Counts are cases, not distinct bugs.

| Area | Before correction | After correction, ten shuffled race-enabled runs |
| --- | --- | --- |
| Memory resolver/converter | 5 fail, 4 pass | 90 pass |
| DNS admission/verification | 7 fail, 1 pass | 80 pass |
| Billing classifier | 8 fail, 4 pass | 120 pass |
| Renderer default and controls | 1 fail, 2 pass | 30 pass |
| Total | **21 fail, 11 pass** | **320 pass, no failures/skips** |

The positive controls matter: legal boundaries, defined billing states, explicit
renderer policy and safe chains were not rejected merely to turn the negatives
green. Negative memory inputs are checked before persistence, not rejected by
a validating storage mock. DNS is deterministic; no external host is contacted.
Timeout error mapping is tested with a controlled resolver deadline error and
with actual child deadlines/cancellation, not a ten-second sleep per test.

### Validation limits (do not omit these when reporting results)

The available compiler was **Go 1.23.2**. A Go 1.27.1 auto-toolchain attempt failed
resolving `proxy.golang.org`; the repository's `go.mod` was not lowered.

- Policy and billing subject files execute their real implementations. The
  pinned errors-library implementations were copied locally for offline linking.
- The memory closure selects the production converter/helpers and the complete
  `MemoryAfterTurn` method verbatim. Authentication, logging, audit transport and
  SDK/GraphQL DTO dependencies are local fixtures; the backend is an accepting
  capture spy. This is not gqlgen HTTP, authentication or database acceptance.
- The renderer closure executes the production settings loader and verification
  gate with the real policy functions. Configuration/logging/task-envelope
  dependencies are local fixtures. It does not test Viper file/environment
  precedence, a real logger, Redis or the external renderer.
- These local fixtures and closure module files are **not** committed. The
  committed tests use the repository's real imports in a normal checkout.
- Targeted `go vet` and source formatting passed. Full Go 1.27 compilation,
  repository tests, `make lint`, actual GraphQL/Redis/PostgreSQL integration and
  frontend acceptance were **not rerun for this follow-up**. The updated existing
  `library/search/egress_test.go` still needs that full-package run.

The earlier Go 1.27/PostgreSQL/frontend results recorded in
[the parent acceptance ledger](../manual/pr49_acceptance_20260918.md) remain
historical evidence for `0a8975a`, not new acceptance of these edits. Its old G05
compatibility-default paragraph is superseded by the current egress manual.

## Dependency-ready replay

With the repository's toolchain and dependencies installed, run:

```sh
go test -race -shuffle=on -count=10 ./internal/library/fileio \
  -run '^TestMemoryAfterTurn(RejectsNegativeBoundaries|PreservesValidBoundaries)$'
go test -race -shuffle=on -count=10 ./internal/library/toolpolicy \
  -run '^Test(AdmissionBoundsEveryDNSLookup|ReportedChainBoundsEveryDNSLookup|AdmittedAddresses)'
go test -race -shuffle=on -count=10 ./library/billing/oneapi \
  -run '^TestClassifyBillingOutcome(RejectsInvalidTypedStates|PreservesDefinedStates)$'
go test -race -shuffle=on -count=10 ./library/search \
  -run '^Test(DefaultEgressRejectsMissingRequestChain|LoadEgressSettingsDefaults|VerifyRenderedEgress)'
go test -race -cover ./...
make lint
```

For the red control, create a separate worktree at the parent SHA and copy only
the four new `*_review_test.go` files there. They call existing stable entry
points, so a converter signature change is not the reason for the old failures.
Do not replace production validators with mocks or lower the module version.

## Rollout and scope

An old renderer that omits RequestChain now fails verification when configuration
is absent. Deploy a renderer that enforces its connection policy and reports
its chain before relying on successful fetches. An already explicit `false`
override must be removed or changed; a secure default cannot override explicit
operator intent without an additional policy change.

Post-response verification cannot prove that a separate worker pinned sockets,
blocked every subresource or truthfully reported every connection. External G05
acceptance remains open. Rejecting a body does not undo a connection the renderer
already made, and no such guarantee is claimed here.

No CI/workflow/benchmark trigger, dependency, lockfile, module version, database
migration, generated GraphQL source, MCP enable flag or pricing rule is changed.
No paid provider call, production mutation, merge or deployment is performed.
