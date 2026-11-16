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
`library/log`, `library/throttle`, `library/httpbody`, `library/httpclient`,
`library/billing/oneapi`, `internal/web`,
`internal/library/toolpolicy`, `internal/library/fileio`, `internal/mcp`,
`internal/mcp/auth`, `internal/mcp/askuser`, `internal/mcp/userrequests`,
`internal/library/ssoredirect`,
`internal/web/telegram/formatting`, `internal/web/telegram/controller`,
`internal/web/telegram/dao`, `internal/web/telegram/service`, `library/clientip`,
`internal/web/blog/service`, `internal/web/blog/oneapi`, `internal/web/blog/controller`,
`internal/mcp/imageproc`, `internal/mcp/storage`, `internal/mcp/tools`,
`internal/web/arweave/controller`, `internal/web/general/controller`, `library/search/firecrawl`,
`library/search/bing` and `library/memlimit`. The MCP ingress
selections cover API key validation at every entrypoint (transport, console APIs,
ask_user console, GraphQL shared fields), caching, flood bounds, outage
fail-closed behavior, production wiring, the prod-mode refusal of disabled
validation, the configured protected-resource metadata URL, upstream budgets
spent only by calls that hold a slot, request-body caps, slow-body and
burst-then-stall cuts, the count of bodies whose read deadline cannot be armed, the
in-flight body budgets of the MCP transport (bodies are classified by the
credential gate before they are charged, so anonymous and rejected callers
never spend the verified budget; one key cannot exceed its share; only bytes
actually read are charged, so stalled senders reserve nothing; reads beyond
a budget are refused; release on return and on panic; never overcharged
under races), the cheap refusal of an oversized Authorization header, the
DEBUG classification of client-aborted uploads, the
startup bounds of the validation budgets, and held requests that outlive the read budget. They
also cover the anonymous access boundary: the transport allowlist, refusal of
anonymous tool calls before any handler, and the SDK dispatch check for every
registered tool. They also check that no caller can cancel another caller's
request, while legacy sessions keep their own cancellations, and that the
isolated cancellation policy decodes bodies exactly like the SDK. Other selections
keep credentials off redirect targets for the validator and the consume call.
The third-review selections pin the credential inventory (`TestIngressInventory`:
every credential reader, one-api REST call and identity construction is
registered), the one-api window shared with consumes, gateway and named
(Telegram) source budgets, the verbatim legacy balance endpoint, stale grace
only for authority failures, the length bound before hashing, and the
Telegram, ArweaveUpload, balance-proxy and `ValidateOneapiAPIKey` paths
through the shared authenticator. The independent-review selections pin the
outage window (stale grace for every known key through an outage, closed by
the first verdict, never opened by a per-token failure or a local refusal),
exact refunds of refused charges, newer rejections winning over late
acceptances, sealed verified identities, the scanner's indirect spellings,
and redirect refusal on credential-carrying search clients.
They check that only one-api's success envelope on a consume refreshes an
active key's verdict, and that the refresh never revives a rejected key. The
request-body read-credit arithmetic runs on a fake clock. Real-socket timing
variants stay in `make test` and are not selected:
`TestReadDeadlineCutsSlowBodyButNotTheResponse`, `TestMinDataRate*`,
`TestAbsoluteReadCapBoundsFastEnoughClients`, `TestBurstThenTrickle*`,
`TestPauseWithinTheIdleBoundIsAdmitted`, `TestReadDeadlineCutsSlowBodyOverHTTP2`,
`TestIngressSSEStreamOutlivesReadBudget`,
`TestIngressCutsBurstThenStallWithinTheIdleBound`,
`TestGinBodyBudgetCutsBurstThenStallThroughTheProductionChain` and
`TestProductionEngineAdmitsSlowSteadyLargeUploads`. The capacity simulation
`TestDefaultBudgetsCapacityForActiveKeys` is not selected either. On a Linux
16-thread host with a warm build cache, this trimming took the selected
PR #95 tests from about 10.8 s to about 3.3 s of test time. The full gate went
from 78.3 s to 70.6 s. These exercise input validation, controlled
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

The user-request image selections (issues #83 and #87) cover first-frame-only
GIF decoding with frame-count-independent allocation, the decode memory
budget (contract coverage, rejection before allocation, queuing, backfill
past a large waiter, no starvation, per-identity caps, header-only bombs),
byte-identical single-copy EXIF orientation and the hostile-IFD parser fix,
TIFF layout parsing, no object left without an image ref after every
post-upload failure, quota reservation before upload, no transaction held
across uploads, the delete lease against late deletes, lost reservations,
shared-object safety, expired-ref revival charging, the GC revival race, the
claims-driven reconciler (crashed uploads, protected and unclaimed keys), the
image create caps, and lifecycle rules preserved or left untouched on shared
buckets. After the second review of PR #94 they also cover one regression per
claim-lifecycle invariant (GC batches that roll back together, the late-write
re-check of an abandoned PUT, the last releaser of a shared key, re-pointed
refs, admission before the body is read and the bounded wait queue), the
decoder layouts the estimator undercounted (late PNG tRNS, Adobe APP14 after
the first scan, a stray `\xff\x00` before a fake frame header, lossless
VP8L in VP8X, truncated VP8 chunks), a slow body that must give its slot back
at the deadline, and collision-free, bounded, unique lifecycle rule IDs. After the maintainer review of PR #94 they also cover TIFF tile geometry that used to wrap the estimate and a negative estimate failing closed, bounded parallel uploads that meet the request deadline, refusal without time to upload and siblings canceled after a failed PUT, a lifecycle rule lost to a concurrent writer and merged again, a writer that keeps replacing it, no rewrite of an unchanged rule, the read-only verify check, the lifecycle controller restoring an erased rule and never writing in verify mode, the reconciler draining a backlog larger than one batch (also past a key whose delete keeps failing) and exporting backlog and failures, a rule erased between checks being reported, a panicking upload being compensated, the `lifecycle_mode` validation, and the image memory envelope (its terms, sizing a defaulted decode budget to half the memory limit, normalized PNGs kept without encoder slack, cgroup v1/v2 limit detection and the Go soft limit derived from the container limit, `library/memlimit`). After the third review of PR #94 (second maintainer review of a7c7580) they also cover quota accounting that charges an image once
while several requests reserve it (and keeps it charged when one of them fails),
a quota report whose `reserved_bytes`/`available_bytes` match admission and the
enforced defaults, 5xx image errors that carry a fixed message instead of the
internal error chain, the per-identity quota lock that does not depend on the
key prefix, and the additive `sha256` column of the claims table. The other new tests of those
packages (GC batching, re-check of a later committed upload, concurrent
reconciler deletes, streamed attachments, legacy rule IDs, the first ALPH
chunk, stale claims kept while the reconciler runs) run in `make test`. The estimator-versus-allocation sweep, the timing-based heavy/light
admission test, the fuzz targets and the PostgreSQL tests (`TestPostgres*`
with `MCP_USER_REQUESTS_TEST_POSTGRES_DSN` set to a disposable database; they
cover the pool-starvation repros, REPEATABLE READ, the quota across two key
prefixes, and compensation and the reconciler held at the interleavings that
matter, asserting that the competing creates succeed, plus a quota report read from
one snapshot, ref row locks in one order for finalize and the GC, and a boot
that takes no lock on the migrated claims table), the MinIO lifecycle acceptance (`TestMinIOLifecycleAcceptance` with `MCP_STORAGE_TEST_MINIO_*` pointing at a disposable bucket) and the concurrent-create memory measurement (`TestImageCreateMemoryEnvelope` with `MCP_IMAGE_MEMORY_ACCEPTANCE=1`) stay deliberate
acceptance. The `user-request-images-postgres` workflow (`workflow_dispatch`)
runs the PostgreSQL tests on any ref against a disposable PostgreSQL 17 and
fails when one of them skips:

```sh
go test -count=1 ./internal/mcp/imageproc -run 'TestEstimateDecodeBytesBoundsActualAllocation|TestDecodeBudgetHeavyCallerCannotStallLightCaller'
go test ./internal/mcp/imageproc -run '^$' -fuzz FuzzReadJPEGOrientation -fuzztime 30s
go test ./internal/mcp/imageproc -run '^$' -fuzz FuzzEstimateDecodeBytes -fuzztime 30s
MCP_USER_REQUESTS_TEST_POSTGRES_DSN=postgres://... go test -race -count=3 ./internal/mcp/userrequests -run TestPostgres
MCP_STORAGE_TEST_MINIO_ENDPOINT=127.0.0.1:9000 MCP_STORAGE_TEST_MINIO_BUCKET=... MCP_STORAGE_TEST_MINIO_ACCESS_KEY=... MCP_STORAGE_TEST_MINIO_SECRET_KEY=... \
  go test -count=1 ./internal/mcp/storage -run TestMinIOLifecycleAcceptance
MCP_IMAGE_MEMORY_ACCEPTANCE=1 go test -count=1 ./internal/mcp/userrequests -run TestImageCreateMemoryEnvelope -v
```

The client-identity selections cover the trusted-proxy policy (defaults, explicit
empty list, invalid entries failing startup), the right-to-left forwarding-chain walk
through the production engine, direct peers that cannot choose their adaptive-authentication
risk key, IPv6 /64 grouping, and the bounded account-keyed failure budget that survives
client-address rotation ([client identity](client_identity.md)). They also cover a request
host that can select but never disable the Turnstile pair, `X-Forwarded-Host` honored only
from trusted proxies, in-flight reservations against concurrent guesses, collation-folded
and identity-unified account keys, complete-pair validation, legacy mutation retirement, alias reset and concurrent reservation preservation, and a flood that cannot evict an attacked account.
They also cover accounts that fold to one spelling but are distinct in the store:
a sign-in to one never clears or orphans the other's evidence, including after a
link flip, under a concurrent burst, through a stale identity-index entry, for
identity-less email-code failures, and after the link bound overflows. A further
contract test checks that the fold never splits inputs the MongoDB lookup treats
as one account.
Registration codes count against the email from rotating addresses, and the shared
hostname rule and the trusted-proxy startup warning are covered too. The
`internal/web/blog/service` and `internal/web/blog/oneapi` selections run on in-memory
SQLite. They prove that one emailed code is compared at most five times, sequentially
or concurrently, that wrong and storage-failed registration codes are told apart, and
that codes stored before the attempts column existed remain usable. The MongoDB forms
of these tests run in the manual MongoDB contract job.

The SSO selections run the shared redirect-policy and registry vectors that the
frontend tests also consume, the registered-client code issuance and redemption
bindings, the start and completion re-checks of GitHub and passkey sign-in
(including the completing-issuer check at GitHub start), private development
targets, backslashes after the query or fragment, and startup validation of the
redirect settings, all in process with miniredis.

The Telegram selections also cover cancellation after refill, overlapping
reservations, isolation of invalid credentials from delivery capacity, remote
credential first use, canceled contexts, requests that end after authentication
but before a send is kept, malformed directory loader results, and the bound on
requests waiting for a slow directory read, both in the directory and through
the GraphQL and webhook entrypoints. They also cover the directory's read
cadence under anonymous misses, the prompt rereads that remain, remembered
fallback verifications, a read deadline that holds when the loader overruns it,
the read summary, the bounded cursor close, and how the start-up index step
judges an index already present. The cursor close is also checked against
MongoDB by `TestMongoAlertCredentialCursorCleanupIsBounded` in the manual
`mongo-query-contract` job, and the monitor indexes by
`TestMongoMonitorIndexesServeAlertLookups`,
`TestMongoConcurrentAlertTypeCreationKeepsOneDocument`,
`TestMongoMonitorIndexesKeepStoredData` and
`TestMongoTelegramServiceEnsuresMonitorIndexes`.
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

The FileIO schema selections exercise serialized tools/list and find_tool,
complete write/restore union alternatives, SDK-validated mandatory conditions,
and that each alternative declares exactly the arguments it accepts. They use
synthetic arguments and no storage, network or database. Real mutation
concurrency, database acceptance and native-client probes remain deliberate
local/manual work.

MCP request diagnostics also run in the automatic fast gate: the actual dependency
error hook through a synthetic alert receiver, production NewServer logger wiring,
typed-cause/protocol classification, SDK method-allowlist parity, alert-to-OTLP
failure_id correlation, FIFO-barrier exactly-one-alert checks, expected quiet
probes, sealed-envelope spoofing and safe correlation normalization.
