# Automatic CI and manual acceptance

Effective 2026-10-08, the owner requests formatting and the fastest essential
unit tests on each update. Heavy integration, race repetitions, performance,
stress, fuzz, live-provider and environment acceptance run deliberately on a
developer machine or staging, rather than on every push or pull request.
This changes when tests run, not their assertions or acceptance criteria.

Current fast, quality, network-security and compatibility/database acceptance
CI jobs select Go 1.27.2 explicitly, independently of the unchanged Go 1.27.0
module and language floor. The [2026-10-08 patch release](https://go.dev/doc/devel/release#go1.27.0)
includes security fixes. Historical timing measurements below retain the
toolchain versions actually measured; they are not relabelled as Go 1.27.2 runs.

The automatic `Linter / check` runs read-only gofmt and goimports checks plus
the named unit tests listed in `.scripts/fast_ci_tests.json`, covering `cmd`,
`library/log`, `library/throttle`, `internal/web`, `internal/library/toolpolicy`,
`internal/web/telegram/formatting`, `internal/web/telegram/controller`,
`internal/web/telegram/dao` and `internal/web/telegram/service`. These exercise input validation, controlled
DNS admission, pinned addresses, redirect limits, cancellation, credential-safe
logging, Telegram escaping, complete alert content, document bounds and visible
API rejection without external services. Startup and actual-caller controls also cover
eager bound-context snapshots, full large selected input, legacy hook behavior,
whole-record rejection and quiet successful/unauthorized paths using synthetic
loopback HTTP only. All prior 20 essential tests remain selected. The eight
`library/log` OTLP cases use disposable loopback TLS receivers to verify operational
message/field fidelity, unaltered synthetic canaries, native nested/duplicate fields,
disabled callback behavior, immutable With snapshots, accepted-entry decisions,
future-field preservation, custom resource attributes with explicit identity precedence
and absence of implicit truncation. The explicit external-delivery test and
queue/outage/race campaigns remain deliberate acceptance. The explicit manifest
is `.scripts/fast_ci_tests.json`. The runner rejects missing discovery, skipped
or failed tests, unsuccessful processes and missing package completion. Its
JSON receipt records source, toolchain, command exits and elapsed time. Counts
and selected tests must be deliberately reviewed when changing the manifest.

The Telegram selections also cover cancellation after refill, overlapping
reservations, isolation of invalid credentials from delivery capacity, remote
credential first use, canceled contexts, requests that end after authentication
but before a send is kept, malformed directory loader results, and the bound on
requests waiting for a slow directory read, both in the directory and through
the GraphQL and webhook entrypoints. They also cover the directory's read
cadence under anonymous misses, the prompt rereads that remain, remembered
fallback verifications, a read deadline that holds when the loader overruns it,
the read summary, and the bounded cursor close. The cursor close is also checked
against MongoDB by `TestMongoAlertCredentialCursorCleanupIsBounded` in the
manual `mongo-query-contract` job.
The manual sweep benchmarks can be run with
`go test ./library/throttle -run '^$' -bench 'Benchmark(SweepRetainedTitles|AdmissionDuringSweep)' -benchmem`.
On a Windows Ryzen 7 5700G, two 500 ms runs scanning retained, non-full buckets
took 30–40 µs at 1,000 titles and 348–379 µs at 10,000 titles, with no sweep
allocations. Forcing a sweep on every parallel admission took 42–43 µs/op and
313–446 µs/op respectively; those are aggregate throughput figures, not tail
latency. Sweeps hold the throttle mutex, so production latency depends on load.
For comparison, forced credential-directory refreshes over an in-memory loader
took 2.331 ms and allocated 1.24 MB for 1,000 entries, and took 16.09 ms and
allocated 12.11 MB for 10,000 entries. This was one 1-second run while other
packages compiled. The directory no longer keeps a second digest per alert name.
After that change, three 1-second runs on a Linux 16-thread host took 0.96–1.02 ms
and allocated 0.63 MB at 1,000 entries, and took 8.5–9.2 ms and allocated
6.18 MB at 10,000 entries. On the same host and runs, the previous code took
1.86–1.89 ms (1.24 MB) and 17.7–18.6 ms (12.11 MB). A hot miss took 0.8–1.0 µs
both before and after. These measurements exclude MongoDB and do not establish
production latency:

```sh
go test ./internal/web/telegram/controller -run '^TestAlertCredentialDirectoryRejectsNilLoaderEntry$' -bench '^BenchmarkAlertCredentialDirectoryMiss$' -benchtime=1s -count=1
```

The Go team's [`rate.Reservation.CancelAt` documentation](https://pkg.go.dev/golang.org/x/time/rate#Reservation.CancelAt)
describes cancellation in the presence of subsequent reservations. It is a
useful comparison for reservation semantics; the local limiter retains its
own atomic shared and per-title admission contract.

```sh
python3 .scripts/test_fast_ci.py
python3 .scripts/fast_ci.py --evidence /tmp/fast-unit
```

`dependency-compatibility`, `mongo-query-contract` and `fileio-project-picker`
retain their complete job bodies as optional `workflow_dispatch` acceptance on
the chosen ref. Local equivalents remain supported:

```sh
# Full Go behavior, race and coverage; complete read-only quality checks.
make test
make lint

# Protocol and repeated session ownership acceptance.
go test -race -count=1 -timeout 5m -v ./internal/mcp -run 'TestMCPStableSDK|TestServerSupportsMCPProtocol|TestServerKeepsLegacyMCP'
go test -race -count=20 -timeout 5m ./internal/mcp -run TestMCPReview
go build ./...
go vet ./...
make gen
go mod tidy
git diff --exit-code -- go.mod go.sum internal/web/generated.go internal/library/models/models.go

# Set MONGO_QUERY_TEST_URI to disposable MongoDB 8, never production.
go test -race -count=3 -v -timeout 5m ./internal/web/blog/service ./internal/web/twitter/service -run TestMongo
go test -race -count=1 -v -timeout 10m ./internal/web/blog/controller ./internal/web -run TestMongo

# Set FILEIO_TEST_POSTGRES_DSN to disposable PostgreSQL 16 with pgvector.
go test -race -cover ./internal/mcp/files ./internal/mcp/auth
go vet ./internal/mcp/files ./internal/mcp/auth

# Run frontend acceptance from web/ using locked dependencies.
cd web
pnpm install --frozen-lockfile
pnpm test
pnpm lint
pnpm build
```

An environment-dependent suite can skip when its URI is absent. Such a local
run is not evidence of database acceptance: use the disposable database or the
manual workflow, which supplies it. Preserve the full test assertions and
report skips and failed exits honestly. Fuzzing stays deliberate, for example
`go test ./internal/library/toolpolicy -fuzz FuzzURLForLogOriginOnly -fuzztime 1m`.
Benchmark/evaluation targets (`make memory-bench-test`, `make memory-bench-current`,
`make memory-bench-live PLUGIN=rag`, `make eval-plugin PLUGIN=rag`) remain manual.
Live runs require deliberate operator configuration and must never receive
production credentials through the fast unit gate.

The 2026-10-09 owner-approved simplification also makes `project-quality`,
`mcp-network-security` and CodeQL opt-in with `workflow_dispatch`. Their complete
job bodies remain in source: uncapped Go diagnostics, symbol/package vulnerability
scans, vet and pure-Go/SQL-owner invariants, frontend lint/dependency audit,
frontend test/build acceptance, and the three-run network security race suite.
CodeQL no longer runs on master/develop push or PR events or its former Saturday
schedule. This extends PR #56's earlier cadence change; it deletes no coverage.

`Linter / check` remains the single automatic formatting and essential-unit
gate on every PR and master/develop push. `ci.yml` image publishing/deployment
and the path-filtered `deployment-contract.yml` safety job stay byte-for-byte
unchanged. Dependency/database acceptance, memory benchmarks and evaluation
already use manual dispatch and remain unchanged.

In the checked-in YAML, a Go-changing PR targeting master/develop falls
from five to one: Linter (one), project-quality (two), MCP network security (one)
and CodeQL (one) become Linter only. The old project-quality frontend-acceptance
job was skipped on PRs and is excluded from this execution count. PRs touching
deployment wiring still add the unchanged shell-contract job. A typical master
source push falls from two validation jobs to one, alongside the unchanged four
delivery jobs. The Saturday CodeQL schedule falls from one job to zero. These
counts describe declared YAML scheduling, not account settings. A read-only
Actions inventory found CodeQL already `disabled_manually`. Therefore the active
checked-in Actions count for a typical Go PR is four to one, and master validation stays
one to one alongside four delivery jobs. CodeQL remains disabled; this PR does
not enable it or repair its inherited v1 actions. Its preserved commands become
manual-only in source if an owner separately enables the integration later.
The independent account-managed CodeQL default setup still runs four language
analysis jobs via `dynamic/github-code-scanning/codeql`. Those external automatic
checks are outside this repository-only amendment and remain unchanged.

Before editing, an authorized read of master branch protection returned
`required_status_checks: null` and the repository ruleset list was empty.
No branch protection, account/integration settings or credentials were changed.
GitHub's [workflow-dispatch documentation](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows#workflow_dispatch)
describes manual selection of a ref. An enabled workflow with a dispatch trigger
must first exist on the default branch to be dispatched; use the unchanged local
commands for this unmerged PR. CodeQL's existing disabled state is an additional
manual-execution limitation, not changed by this repository patch.
[Required-check documentation](https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax)
warns that removing a required workflow trigger can otherwise leave a check
pending. Recheck protection before any later merge; this PR does not change it.

The complete frontend `pnpm test` suite and `pnpm build` production build run in
the `project-quality / frontend-acceptance` job only on `workflow_dispatch`, or
locally using the commands above. Its locked dependencies, tool versions and
test/build commands are preserved. `frontend-quality` keeps `pnpm lint` and the
all-severity dependency audit in the same manual workflow; Go quality and
security commands are unchanged. Manual dispatch runs qualification and the
retained quality jobs.

This policy supersedes earlier statements that database/compatibility acceptance
must run per PR, and the nightly/per-PR evaluation wiring described in proposal
section 7.7 and the eval runbook. Evaluation workflows already use manual dispatch.
Historical acceptance receipts stay evidence for their recorded source only;
the quick gate does not claim full release acceptance.

Initial measurements on Go 1.27.0 Windows with a warm module cache: the selected
two-package command (JSON, count=1, timeout=60s, run='^Test') passed all 16 tests
with zero skips in 40.679 seconds using a fresh isolated build cache and 4.119
seconds using that cache again. These exclude formatting, Actions provisioning
and module downloads; they are not hosted-runner timings. The prior PR #55
compatibility job took about 7m37s; its complete body remains manually available.
