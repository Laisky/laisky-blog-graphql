# FileIO versioned editing

Updated: 2026-09-17. This is the mandatory external concurrency contract for PR #45.
It supplements `mcp_files.md` and the FileIO requirements/architecture manuals;
its version fields, mandatory mutation preconditions, and UTF-8 rules supersede
older unversioned response examples and optional-client-compatibility statements.
It does not change tenant authorization or storage quotas.

## Client protocol

`file_read` returns `content`, `content_encoding`, and `version` together. The
version describes those bytes from the same database row snapshot. `file_stat`
returns a version for an existing file, but not for directories or missing paths.
`file_write` returns `bytes_written` and the version committed by that request.
Treat versions as opaque strings, never as JSON numbers or history snapshot IDs.

Read, compute the intended change, then submit it with `expected_version`:

```json
{
  "project": "notes",
  "path": "/state.json",
  "mode": "TRUNCATE",
  "content": "{\"count\":1}",
  "expected_version": "76c3e40a3c4b4f5d9c2617d1b8094593:42"
}
```

Use the actual token returned by your read, not the illustrative token above.
A winner returns a new version. A stale request returns an MCP tool result with
`isError=true` and a JSON payload such as:

```json
{
  "code": "VERSION_CONFLICT",
  "message": "file version changed; re-read the file and recompute the edit before retrying",
  "retryable": false
}
```

The unchanged request is not retryable. Re-read and recompute the edit; do not
merely substitute the new token into the old payload or reuse an old byte offset.
A rejected condition does not update content, revision, history, or the indexing
outbox. Do not obtain the edit token using a later, separate stat: that can pair
old content with a newer version.

| Operation | Condition and behavior |
| --- | --- |
| `file_write` | **Required:** exactly one of `expected_version` for an existing file or `create_only=true` for an absent file. Every mode, including APPEND, follows this rule. The tool schema expresses the alternatives with `oneOf`. |
| `file_read` | Optional `expected_version`. Subsequent ranges use the first range's version. Changed, missing, or recreated files fail, including empty/EOF reads. |
| `file_delete` | **Required:** `expected_version` for an exact file. A child-file token never authorizes recursive directory deletion. |
| `file_rename` | **Required:** source `expected_version`. A non-overwriting move requires an absent destination. For `overwrite=true`, additionally supply `expected_destination_version` or `destination_must_not_exist=true`. |
| Historical restore | Condition on the live file's current version; the historical numeric snapshot ID chooses the bytes to restore, not the edit base. |

Rename destination conditions require a source condition and are mutually
exclusive. A same-path rename checks a supplied condition but does not advance
revision. File tokens do not provide directory namespace/read-set validation.
Public directory-wide mutations reject the request rather than falling back to
unconditional behavior. List and mutate individual files with their tokens; this
is not an atomic directory transaction and does not cover new descendants.

Malformed or empty version strings, numeric/null versions, invalid booleans,
unsupported conditional backends, and contradictory conditions fail with
`INVALID_ARGUMENT`; they never silently become blind writes. The native RAG
adapter and PageIndex with its transactional SystemFS support conditions. The
MCP plugin manager resolves a supporting adapter before calling it. Third-party
adapters must explicitly advertise and actually enforce the version-precondition
capability. Missing mutation intent is `PRECONDITION_REQUIRED` with
`retryable=false`, before calling the backend. `create_only=false` alone is not
a precondition. There is no force flag or blind-APPEND compatibility bypass.
Handlers enforce the contract even when a client caches or ignores tool schemas.

## HTTP and Go callers

Existing `PUT /api/file` and `POST /api/versions/{id}/restore` accept a single
strong quoted `If-Match` file token, or `If-None-Match: *` for create-only.
These headers are mandatory. Missing conditions return HTTP 428 with
`Cache-Control: no-store`; stale tokens return 412; malformed conditions return
400. Success includes `version` in JSON and the quoted token in `ETag`.
Weak validators, token lists, multiple condition headers, and `If-Match: *` are
deliberately unsupported. Use the token from the content snapshot being edited,
obtained through FileIO.

Go callers can pass `WriteOpts{ExpectedVersion: read.Version}` or
`WriteOpts{CreateOnly: true}` to `WriteWith`. Other service operations accept a
context produced by `WithFilePreconditions(ctx, auth, project, canonicalPath,
operation, conditions)`. Always check its error. The context is scoped to the
operation, authenticated tenant, project, canonical path, and system owner; use
it only for that operation. Internal indexing reads and system-state mutations
do not inherit a user-write precondition. The internal Service methods are
imperative storage primitives, not an external legacy-client compatibility path.
Public MCP and HTTP adapters always require conditions for mutations. Internal
writes continue to advance revisions so stale external tokens are invalidated.

## Version identity and storage

A token is currently `incarnation_id:revision`: a randomly generated 128-bit hex
identity plus a positive signed 64-bit counter. Each file incarnation starts at
1. Delete/recreate assigns a new identity. Rename preserves identity and advances
revision; moving away and back cannot revive an old token. Restoring old bytes
creates a new revision, not an old live token. Accepted same-content writes also
advance revision. Counter exhaustion fails with `REVISION_EXHAUSTED`, not wrap.

Database triggers maintain this counter for all existing SQL content/path/
deleted-state writers, including legacy application code, user-file operations,
and system namespaces. Summary/index bookkeeping alone does not bump it.
Do not write the identity/revision columns directly in application code or
disable the triggers. Database administrators are outside this trust boundary.

Mutations retain the project's PostgreSQL transaction advisory lock, with
explicit READ COMMITTED isolation. An existing-file UPDATE also checks the
snapshot token in its WHERE clause and requires exactly one affected row. The
new version is captured inside the transaction and returned only after commit;
it is not obtained by a post-commit read that could observe another writer.
All file/history/outbox changes and revision increments roll back together.

## Automatic startup migrations

`NewService` automatically calls `RunMigrations` before returning. No separate
operator migration command is needed. A durable `mcp_file_schema_migrations`
ledger records completion of the complete FileIO schema step, not merely the
revision helper. First migration backfills identities without changing bytes,
installs indexes/triggers and records success in the same transaction.

Completed startups perform a dialect probe and an indexed checkpoint read, then
return. They do not start a migration transaction, acquire migration locks, touch
file rows, scan content, rerun backfill, or recreate tables/indexes/triggers.
PostgreSQL verifies the ledger belongs to the current schema, not another schema
later in `search_path`. This is bounded metadata work, not literally zero SQL.

When a step is absent, startup opens a transaction, acquires a PostgreSQL
schema-scoped transaction advisory lock (or reserves the SQLite writer), creates
the ledger if needed, and rechecks completion before applying changes. Competing
starters skip schema work after observing the winner's committed marker. All DDL
helpers use the same transaction/connection; no nested migration transaction is
opened. Failure/cancellation rolls back both schema work and the marker, fails
startup, and allows automatic retry at the next startup.

The migration budget is two minutes, subject to earlier caller cancellation.
A deployment requiring longer initial work must adjust the budget before rollout;
never record an incomplete migration as successful. First revision installation
still takes an ACCESS EXCLUSIVE PostgreSQL file-table lock. That initial work
can block; subsequent completed-startup checks do not take that lock and should
complete even while another transaction locks the file table exclusively.

Initial schema privileges must permit table alteration, index/trigger/function
creation and backfill updates. Startup does not grant privileges. Permission or
connection errors do not masquerade as an absent checkpoint. An unknown newer
schema version fails closed. Future schema changes must add an ordered numbered
step, not repurpose a completed one or replay the whole baseline. Manual schema
drift with a retained marker is not automatically repaired by scanning all
objects on every startup. No new service, scheduler, or CI is required.

SQLite uses its own writer isolation and may return database busy/snapshot
errors under simultaneous writers. It does not gain PostgreSQL advisory-lock
semantics. SQLite functional tests are not evidence of production PostgreSQL
concurrency; use the PostgreSQL tests for that claim.

## UTF-8 and client behavior

Incoming text and the complete resulting file must be valid UTF-8. OVERWRITE
start/end offsets and read range boundaries cannot split a UTF-8 code point;
these fail with `INVALID_OFFSET`. Invalid stored content fails with
`INVALID_CONTENT` instead of silently becoming replacement characters in JSON.
TRUNCATE with valid text can repair an old corrupt file. Historical content's
existing HTTP base64 recovery path remains available. Binary callers must encode
their bytes as text; arbitrary binary writes were never the text-only contract.

Public calls without mutation conditions are rejected, not supported as legacy
blind writes. Each APPEND also needs the current `expected_version`, or
`create_only=true` for initial creation. Retrying a conditional APPEND with the
same old token cannot append again; it returns conflict rather than replaying
the first success. Durable operation-ID deduplication and automatic merge remain
separate protocols and are not implemented by this change.

During rolling deployment, triggers keep old writers' revision updates visible,
but an old server may ignore new MCP arguments. Route conditional clients only
to upgraded servers, or wait until all serving instances have been upgraded.
Do not advertise safe conditional editing on a mixed old/new request fleet.
The existing UI is not automatically rewritten by this change; integrations
that omit conditions now fail instead of silently overwriting a file.

After restoring a database to an older point in time, old tokens can recur.
Before accepting edits against that restored database, invalidate previously
issued tokens by rotating file incarnation identities under a write freeze and
require clients to re-read. Automated restore/epoch coordination is not included.

## Regression suite and execution

The former unsafe characterizations now assert rejection/integrity for lost
updates, stale offsets, range mixing, lifecycle changes, ABA, and wire content.
Tests also cover creation races, 24 same-token contenders, recomputed increments,
rollback/cancellation, precision beyond 2^53, overflow, strict MCP arguments,
real RAG/PageIndex routing, HTTP conditions, and migration backfill/idempotence.
The original positive append/mixed-write/namespace/quota histories are retained.
The internal imperative-append test is not an external compatibility assertion.
New regressions exercise missing conditions through real MCP/HTTP adapters;
serialized schema requirements; migration fast-path SQL allowlists; read-only
restart; concurrent first startup; rollback/retry; and startup while PostgreSQL
holds an exclusive file-table lock. Nominal transport/billing mock fixtures supply
valid conditions; missing-condition tests use raw requests without fixture defaults.

```bash
# Use the Go version in go.mod and a disposable PostgreSQL database with pgvector.
export FILEIO_TEST_POSTGRES_DSN='postgres://postgres:local-test-password@127.0.0.1:55432/fileio_test?sslmode=disable'
CGO_ENABLED=1 go test -race -shuffle=on -count=3 -timeout=12m -v \
  ./internal/mcp/files -run '^TestFileIO'
go test -race -cover -timeout=15m ./...
make lint
```

Without the DSN, PostgreSQL cases skip explicitly. Existing repository checks
can run the ordinary test packages; no additional CI workflow/job is required.
The initial audit's standalone workflow is removed from the final PR diff.
See PR #45 for tested commit SHAs and actual check results; commands listed here
are instructions, not a claim that every check was executed. The earlier PR
validation predates the mandatory-client/startup-checkpoint follow-up. Do not
attribute those prior Go/PostgreSQL passes to this later patch.

The suite does not establish multi-process HTTP linearizability, response-loss
recovery, database failover, directory snapshot safety, or asynchronous index
worker publication fences. These require their own acceptance, not inferred
success from file-row/outbox tests.

Primary references: [PostgreSQL triggers](https://www.postgresql.org/docs/current/sql-createtrigger.html),
[transaction snapshots](https://www.postgresql.org/docs/current/transaction-iso.html),
[SQLite triggers](https://www.sqlite.org/lang_createtrigger.html), and the
[original research index](../ref/20260916_fileio_concurrency_sources.md).
