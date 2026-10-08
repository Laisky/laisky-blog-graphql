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
16 named unit tests in `internal/library/toolpolicy` and
`internal/web/telegram/formatting`. These exercise input validation, controlled
DNS admission, pinned addresses, redirect limits, cancellation, credential-safe
logging and Telegram escaping without external services. The explicit manifest
is `.scripts/fast_ci_tests.json`. The runner rejects missing discovery, skipped
or failed tests, unsuccessful processes and missing package completion. Its
JSON receipt records source, toolchain, command exits and elapsed time. Counts
and selected tests must be deliberately reviewed when changing the manifest.

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

Existing `project-quality` diagnostics, symbol/package vulnerability scans,
frontend dependency audit, `mcp-network-security`, CodeQL and pure-Go/SQL-owner
invariants remain automatic and unchanged. `ci.yml` image publishing/deployment
and `deployment-contract.yml` safety tests also remain unchanged. Consequently
the overall workflow inventory includes separate safety jobs beyond the fast
unit gate. Altering those is outside this test-cadence amendment.

The complete frontend `pnpm test` suite and `pnpm build` production build run in
the `project-quality / frontend-acceptance` job only on `workflow_dispatch`, or
locally using the commands above. Its locked dependencies, tool versions and
test/build commands are preserved. `frontend-quality` keeps `pnpm lint` and the
all-severity dependency audit automatic; Go quality and security steps are
unchanged. Manual dispatch runs both qualification and the retained safety jobs.

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
