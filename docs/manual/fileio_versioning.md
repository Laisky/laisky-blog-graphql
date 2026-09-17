# FileIO versioned editing

Updated: 2026-09-16. This is the implemented concurrency contract for PR #45.
It supplements `mcp_files.md` and the FileIO requirements/architecture manuals;
its additive version fields and UTF-8 rules supersede their older unversioned
response examples. It does not change tenant authorization or storage quotas.

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
| `file_write` | `expected_version` for an existing file, or `create_only=true` for an absent file; mutually exclusive. Applies to APPEND, OVERWRITE, and TRUNCATE. |
| `file_read` | Optional `expected_version`. Subsequent ranges use the first range's version. Changed, missing, or recreated files fail, including empty/EOF reads. |
| `file_delete` | Optional `expected_version` protects an exact file. A child-file token does not authorize recursive directory deletion. |
| `file_rename` | Optional source `expected_version`. A non-overwriting conditional move also requires an absent destination. For `overwrite=true`, additionally supply `expected_destination_version` or `destination_must_not_exist=true`. |
| Historical restore | Condition on the live file's current version; the historical numeric snapshot ID chooses the bytes to restore, not the edit base. |

Rename destination conditions require a source condition and are mutually
exclusive. A same-path rename checks a supplied condition but does not advance
revision. File tokens do not provide directory namespace/read-set validation;
unconditional directory operations retain their old semantics.

Malformed or empty version strings, numeric/null versions, invalid booleans,
unsupported conditional backends, and contradictory conditions fail with
`INVALID_ARGUMENT`; they never silently become blind writes. The native RAG
adapter and PageIndex with its transactional SystemFS support conditions. The
MCP plugin manager resolves a supporting adapter before calling it. Third-party
adapters must explicitly advertise and actually enforce the optional capability.

## HTTP and Go callers

Existing `PUT /api/file` and `POST /api/versions/{id}/restore` accept a single
strong quoted `If-Match` file token, or `If-None-Match: *` for create-only.
Conflicts return HTTP 412; malformed conditions return 400. Success includes
`version` in JSON and the quoted token in `ETag`. Weak validators, token lists,
multiple condition headers, and `If-Match: *` are deliberately unsupported.
Use the token from the content snapshot being edited, obtained through FileIO.

Go callers can pass `WriteOpts{ExpectedVersion: read.Version}` or
`WriteOpts{CreateOnly: true}` to `WriteWith`. Other service operations accept a
context produced by `WithFilePreconditions(ctx, auth, project, canonicalPath,
operation, conditions)`. Always check its error. The context is scoped to the
operation, authenticated tenant, project, canonical path, and system owner; use
it only for that operation. Internal indexing reads and system-state mutations
do not inherit a user-write precondition.

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

The additive migration backfills identities without changing file content,
installs the unique identity index and triggers in one transaction, and is
idempotent. PostgreSQL migration takes an ACCESS EXCLUSIVE table lock; schedule
startup/migration with this blocking behavior in mind. Existing migration
privileges must include table alteration, index/trigger/function creation, and
backfill updates. No new external infrastructure or service is required.

SQLite uses its own writer isolation and may return database busy/snapshot
errors under simultaneous writers. It does not gain PostgreSQL advisory-lock
semantics. SQLite functional tests are not evidence of production PostgreSQL
concurrency; use the PostgreSQL tests for that claim.

## UTF-8 and compatibility

Incoming text and the complete resulting file must be valid UTF-8. OVERWRITE
start/end offsets and read range boundaries cannot split a UTF-8 code point;
these fail with `INVALID_OFFSET`. Invalid stored content fails with
`INVALID_CONTENT` instead of silently becoming replacement characters in JSON.
TRUNCATE with valid text can repair an old corrupt file. Historical content's
existing HTTP base64 recovery path remains available. Binary callers must encode
their bytes as text; arbitrary binary writes were never the text-only contract.

Old calls without conditions remain explicit blind operations for compatibility.
They advance revisions but **can still overwrite another client's edit**. Upgrade
shared-edit clients to send conditions. A blind APPEND can combine independent
complete records in unspecified serialized order, but retrying it can duplicate
records. Retrying a conditional APPEND with the same old token cannot append
again; it returns conflict rather than replaying the first success. Durable
operation-ID deduplication and automatic merge are not implemented by this change.

During rolling deployment, triggers keep old writers' revision updates visible,
but an old server may ignore new MCP arguments. Route conditional clients only
to upgraded servers, or wait until all serving instances have been upgraded.
Do not advertise safe conditional editing on a mixed old/new request fleet.
The existing UI is not automatically a conditional client.

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
Only the explicitly named legacy blind-append test still expects duplication.

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
are instructions, not a claim that every check was executed.

The suite does not establish multi-process HTTP linearizability, response-loss
recovery, database failover, directory snapshot safety, or asynchronous index
worker publication fences. These require their own acceptance, not inferred
success from file-row/outbox tests.

Primary references: [PostgreSQL triggers](https://www.postgresql.org/docs/current/sql-createtrigger.html),
[transaction snapshots](https://www.postgresql.org/docs/current/transaction-iso.html),
[SQLite triggers](https://www.sqlite.org/lang_createtrigger.html), and the
[original research index](../ref/20260916_fileio_concurrency_sources.md).
