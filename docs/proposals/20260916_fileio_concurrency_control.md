# FileIO concurrent editing: behavioral audit and proposed concurrency control

Date: 2026-09-16. Inspected base: `55c7e4c3acefb72797eccf6d37b0ad831815ad0c`.
Status: **test and design proposal; no production behavior or schema change in this PR**.

## Decision

Keep the existing PostgreSQL project transaction lock. Add optimistic conditional
writes using **file incarnation identity + monotonically increasing revision**.
Also validate UTF-8 boundaries/results and add a separate idempotency key for
retryable mutations. Do not replace this with a process mutex, a Redis lease, or
a CRDT as the first implementation.

A revision detects and rejects a stale edit; it does not merge the edit, validate
its meaning, or tell a caller whether a timed-out request committed. These are
separate requirements. Existing version-history snapshots are recovery data,
not an optimistic concurrency protocol.

## 1. What the current code actually guarantees

`internal/mcp/files/lock.go` opens a transaction and acquires a PostgreSQL
transaction-scoped advisory lock for `(apikey_hash, project)`. Write, delete,
rename, and restore use that lock. `writeWithinTx` loads the current bytes,
applies APPEND/OVERWRITE/TRUNCATE, validates quota, writes content/size/hash,
snapshots the previous file, and inserts its indexing outbox job in the same
transaction. PostgreSQL advisory locks coordinate cooperating connections, not
only goroutines [R1, R2].

`service_stat_read.go` obtains a file's bytes in one SELECT. That can provide an
intact committed generation; it does not make two separate reads a shared
snapshot. READ COMMITTED uses statement snapshots [R1]. Neither `ReadResult`
nor the real MCP read/write handlers supplies a live revision or write
precondition. A write-only transaction therefore cannot protect the earlier
read and computation performed by another client.

The SQLite branch does **not** acquire the PostgreSQL advisory lock. SQLite's
own single-writer/snapshot rules and busy errors are different [R7]. The added
SQLite tests exercise deterministic client histories, not production PostgreSQL
concurrency. No claim of SQLite multi-writer parity is made.

### Findings encoded as reproducible client histories

The following are code-derived findings with executable characterization tests.
Their runtime confirmation is the `FileIO concurrency` workflow log, not this
table alone. `TestFileIOCharacterization` and `TestFileIOMCPCharacterization`
intentionally assert the unsafe current outcomes; PASS means **reproduced**, not
**fixed**. Convert these expectations when the proposed protocol is implemented.

| ID | History / observation | Why storage atomicity is insufficient |
| --- | --- | --- |
| FILEIO-001 | A and B read the same JSON. A increments `count`; B changes `label` from its old copy. Both whole-file writes succeed; A's increment disappears. | The second write is atomic but based on obsolete content. History can recover A's bytes, not prevent their loss. |
| FILEIO-002 | B reads `key=0` and chooses byte 4. A writes `key=漢0`. B overwrites byte 4 with `1`, leaving the continuation bytes of `漢`. | Valid UTF-8 request strings can produce invalid stored UTF-8. Size and SHA-256 can still match the corrupted bytes exactly. |
| FILEIO-003 | B computes byte 5 in `{"n":0}`. A writes `{"note":"new","n":0}`. B writes `1` at byte 5. | The result `{"not1":"new","n":0}` is valid JSON and UTF-8 but edits the wrong field. Syntax/hash checks cannot establish edit intent. |
| FILEIO-004 | Read the first half of A, replace the file with B, then read the second half. | Concatenation is A-prefix + B-suffix, a generation that never existed in storage. |
| FILEIO-005 | Repeat the same APPEND after discarding the first response. | The record appears twice. This models a retry; it is not a transport fault-injection test. |
| FILEIO-006 | Read old path, delete or rename it, then save the old edit at that path. | Create-on-write recreates the path. The API cannot distinguish stale editing from intentional creation. |
| FILEIO-007 | A → B → A; also delete A and create A again. | Content hashes can repeat. A fixed clock demonstrates why timestamp uniqueness must not be assumed; recreation also changes identity. |
| FILEIO-008 | Read the invalid stored UTF-8 through the real MCP handler and JSON encoder. | The response substitutes U+FFFD, so successful wire content differs from stored bytes. This follows `encoding/json` behavior [R6]. |

A→B→A is not automatically a bug for a **bytes-only** equality contract. It is
insufficient for the stricter requirement "this is the same file incarnation and
no intervening mutation occurred." Do not confuse these two contracts.

## 2. Test inventory and interpretation

The four `internal/mcp/files/concurrency*_test.go` files use two Service
instances, two connection pools with multiple connections, real migrations,
real service methods, and no LLM/Redis/network credentials. PostgreSQL tests
create a random private schema and clean up only that schema. Use a disposable
database; the fixture installs `vector` in `public` if absent.

| Test group | Behavioral oracle |
| --- | --- |
| Concurrent create/APPEND | 24 unique Unicode JSONL records, each acknowledged once; exact bytes, no interleaving or missing/duplicate record; both missing and existing initial file. |
| Disjoint OVERWRITE | All 16 fixed-width Unicode regions survive; no server-side lost read/modify/write update. |
| Mixed write modes | Eight rounds; compare the complete history plus final bytes against all six serial orders of three overlapping operations. Not just a checksum of the final file. |
| Readers with active writers | Full reads must equal one of the submitted complete generations; a separate single-row SELECT checks content, size, and hash together. |
| Pre-commit / rollback | A gate holds the actual transaction after content/history/outbox mutation. Another pool sees only old committed state; a contender receives RESOURCE_BUSY. Rollback leaves all three planes unchanged. |
| Namespace / quota | Parent-versus-child creation, quota across distinct paths, and competing rename destinations each have exactly one legal winner. |
| Lifecycle | Delete/write, rename/write, and restore/write outcomes must agree with a legal serial history, including historical preimages and outbox counts. |
| Isolation / rejection | Other tenants and projects progress while a lock is held. Failed offset/quota writes leave no content/history/outbox effects. |
| Client characterizations | Deterministic interleavings and real MCP handlers through the RAG plugin reproduce the limitations above. |

The pre-commit gate wraps `DefaultLockProvider`; it is not a mock lock or a
process-wide mutex. Goroutines return errors to the test goroutine rather than
calling `require.FailNow` themselves. Deadlines bound waits. Fixed timestamps
make history ordering rely on IDs, not sleep-dependent clock separation.

`go test -race` complements these assertions but detects observed Go memory
data races, not application-level lost updates across database transactions
[R5]. Multiple pools test cross-connection behavior, **not** a multi-process
HTTP deployment or database failover. Reader stress accepts submitted complete
generations; the explicit held-transaction test supplies the deterministic
uncommitted-visibility check.

Not covered here: real HTTP authentication, network response loss/server kill,
replica lag or failover, non-RAG plugin-specific write adapters, index worker
publication races, concurrent migrations, and exhaustive linearizability of
arbitrary long histories. Outbox assertions do not mean workers have been tested.

## 3. Alternatives and trade-offs

These are design judgments for this repository, informed by the primary sources
in [the reference index](../ref/20260916_fileio_concurrency_sources.md).

| Approach | What it solves | What it does not solve / cost | Recommendation |
| --- | --- | --- | --- |
| Existing transaction/advisory lock | Serializes cooperating mutations, namespace checks, quota, snapshots and outbox. | Cannot know whether client input came from an old read. Project-wide contention remains. | Retain as the existing storage boundary. |
| Monotonic revision + incarnation + atomic comparison | Rejects stale full-file and byte edits, distinguishes recreation and mutation ABA. Simple server-side comparison. | Requires client propagation, all-writer participation, and explicit conflicts; unrelated edits to one file also conflict. | **Preferred first step.** |
| Content hash as strong validator | Rejects writes when bytes differ from the client's base; existing SHA-256 is reusable for integrity. | Equal content after A→B→A or recreation can match. Must define metadata/lifecycle scope. Hash alone is not idempotency. | Useful for byte equality, not a substitute for the chosen mutation-generation contract. |
| `updated_at` / snapshot-history ID | Cheap to expose existing metadata. | Clock precision/repetition; history retention and global snapshot IDs are not the current file revision. | Do not use as the concurrency token. |
| SERIALIZABLE isolation only | Protects operations inside one database transaction; serialization failures require whole-transaction retry [R1]. | A client's earlier read is outside the later write transaction. Holding a transaction through agent thinking is not practical. | Not a replacement for conditional writes. |
| Long-lived edit lock / distributed lease | Can reserve an editing session when every writer cooperates. | Crashes, expiry, fairness and stale-owner fencing; holding a DB connection while an agent thinks is costly. A process mutex misses other replicas. | No new lock service for this requirement. |
| Conditional patch / three-way merge | Can retain independent field/text edits using a known base; JSON Patch has `test` [R9]. | Offsets/arrays can shift; domain invariants and overlapping edits still require rejection/review. A plain patch without checks is not safe. | Optional second step above revisions. |
| CRDT / OT | Designed for concurrent collaborative operations rather than replacing a whole string; Automerge exposes remaining value conflicts [R10]. | New data model, operation identities/history, client protocols, storage/GC and semantic conflict policy. Arbitrary file correctness does not follow from convergence. | Only for an explicit real-time collaborative-editor product requirement. |
| Idempotency key + durable result | Prevents the same logical request from producing duplicate effects after retry [R8]. | Does not protect a new logical request based on stale content. | Complement revisions, especially for APPEND. |

Conditional writes are an established pattern, not a speculative 2026-specific
invention: HTTP If-Match/If-None-Match [R3], GCS generation preconditions [R4],
and Kubernetes resourceVersion conflicts [R11] provide relevant precedents.
An ETag is a protocol validator, **not necessarily a content hash**; the proposed
incarnation/revision token can supply strong validation semantics.

## 4. Proposed minimal contract

### 4.1 Version identity and atomic comparison

Add a positive `BIGINT revision` to the live file. Increment it in the same
transaction for every accepted write (including identical bytes), restoration,
and path/deleted-state change. An actual no-op rename need not increment it.
Failed/rolled-back mutations must not advance it. Idempotent replay must not
increment it again. Detect integer overflow; never wrap or reset a live revision.

Expose an opaque string `version`, conceptually `incarnation:revision`. Use a
non-reused file incarnation (for example a UUID). Existing row IDs are sufficient
only with a documented no-reuse guarantee; database restore/sequence reset needs
an epoch policy. A per-incarnation counter may restart at 1 for a new file
because its identity is different. If the product instead requires one continuous
counter **per path across deletion**, retain a durable path generation/tombstone;
do not derive it from prunable history or reset it when the row is purged.

Consecutive committed increments are easy with a transactional row counter.
Global gap-free numbering is unnecessary and should not become a scalability
requirement; PostgreSQL sequence allocation is not rolled back [R1]. Return
versions as strings and do not parse them through JSON float64/JavaScript Number.

`file_read` returns `{content, content_encoding, version}` from one row snapshot,
including empty or EOF ranges. `file_stat` may also expose version, but clients
must not read content and later obtain a token with a separate stat: that could
pair old bytes with a newer token.

`file_write` accepts mutually exclusive `expected_version` or `create_only`.
Under the existing project lock, require the active file to match identity and
revision, or require absence for create-only. Compare **before** creating history,
index jobs, or any other side effect. A conditional existing-file UPDATE should
also carry identity/revision in its WHERE clause as defense in depth:

```sql
-- Illustrative new schema, not executable against today's schema.
UPDATE mcp_files
SET content = $1, size = $2, content_hash = $3,
    revision = revision + 1, updated_at = $4
WHERE apikey_hash = $5 AND project = $6 AND path = $7
  AND system_owner = $8 AND deleted = FALSE
  AND incarnation_id = $9 AND revision = $10
RETURNING revision;
```

Check the affected row/RETURNING result. Capture the successful revision inside
the transaction and return it only after commit; a post-commit re-read might
return another writer's revision. Scope all comparisons and idempotency lookups
to the authenticated tenant/project/system owner. A supplied stale token for a
missing/recreated file must conflict, not fall through to create-on-write.

Example proposed flow:

```text
A: read -> content X, version "file-identity:42"
B: read -> content X, version "file-identity:42"
A: write expected_version="file-identity:42" -> success, version 43
B: write expected_version="file-identity:42" -> VERSION_CONFLICT; no effects
B: re-read version 43, recompute/merge intended edit, submit against version 43
```

Use a structured MCP tool error (`is_error=true`, `code=VERSION_CONFLICT`,
`retryable=false` for the unchanged request), with a clear re-read/recompute action.
A REST adapter can map a failed precondition to HTTP 412. Do not silently rebase
an old byte offset, strip the precondition, or retry the old full payload using
the new revision. That recreates the lost-update bug.

### 4.2 Every mutation and every range must participate

Delete and restore need the current-file token, not merely the snapshot being
restored. Rename needs the source token and either a destination token or
create-only destination check. A rename should invalidate old path tokens even
if the file is later moved back. Directory rename/delete requires a namespace
revision or a complete validated read set, including phantom descendants; a
single child-file revision is insufficient. Keep this explicit rather than
advertising an unconditional directory operation as concurrency-safe.

For multi-call reads, send the first read's version with subsequent ranges;
reject a mismatch, or read an immutable retained generation. Retention must be
part of an immutable-read contract. Do not hold a transaction across RPCs.

True APPEND computes EOF under the lock and can safely combine independent
records without a read token, provided the caller accepts serialized but
unspecified record order. This is different from an edit calculated from a
previous read. Document a deliberate blind-append contract; idempotency still
matters. Each append must be a complete valid UTF-8 unit, not half a code point.

### 4.3 Content integrity and retry identity are separate

Validate the entire resulting UTF-8 byte sequence before committing, including
an OVERWRITE's untouched suffix. Reject invalid input and invalid byte boundaries
with a stable error. For byte-range reads, either require UTF-8 boundaries or
introduce an explicit byte-preserving encoding; never silently replace bytes or
change the requested offsets. JSON correctness is application-specific: FileIO
can preserve exact bytes and reject stale bases, but cannot generally determine
whether an LLM's new JSON is the intended business change.

For retryable mutations, accept a client operation ID. Persist the request
fingerprint and committed response in the **same database transaction** as the
mutation. The fingerprint includes the operation, normalized paths, bytes/mode/
offset, preconditions and owner. Same scoped ID + same fingerprint returns the
stored response; same ID + different fingerprint is an error. Check an existing
idempotency result before testing a now-stale precondition. Define expiration
and replay behavior after deletion. An expired deduplication record is not an
unbounded exactly-once guarantee [R8].

Keep `content_hash` for integrity/content deduplication. Audit asynchronous
publishers separately: stale jobs should validate the appropriate identity,
revision and path/deleted state before publishing. Equal bytes can legitimately
reuse content-only artifacts, but cannot authorize stale lifecycle effects.
This PR verifies outbox records, not those worker fences.

## 5. Rollout and acceptance

Use additive columns and response fields first. Inventory all writers: MCP,
HTTP/UI, internal WriteWith/restore, memory-manager/plugin adapters and system
namespace writers. Route them through one conditional-write implementation.
During a rolling deploy, old writers must not mutate without advancing the new
revision; gate enforcement until all serving writers understand it. A migration
plus a new MCP parameter alone is not sufficient.

Preserve legacy unconditional calls temporarily with explicit documentation and
metrics, then enable strict preconditions for shared-edit clients/projects.
Optional checks cannot promise protection against arbitrary blind writers.
Reject malformed/unknown conditional values rather than silently treating them
as absent. Separate conflict metrics from lock timeouts and backend failures;
never log file contents or credentials.

Acceptance for the implementation follow-up:

- N writers with the same existing-file token: exactly one success, N-1 typed
  conflicts; no losing snapshots/jobs/revision bumps. Re-read/recompute retries
  preserve every intended independent edit.
- Create-only contention, stale delete/rename/restore, delete/recreate and
  rename-away/back reject obsolete tokens. Directory operations validate their
  complete namespace/read set. Same bytes after ABA still have a new mutation token.
- Read content and token are one snapshot; all range reads with a pinned token
  are coherent or fail explicitly, including empty ranges and EOF.
- Invalid UTF-8/boundaries, quota failure, cancellation and rollback leave all
  content/metadata/history/outbox/version state unchanged. Test revision overflow
  and values beyond JavaScript's exact-integer range.
- Response loss followed by identical operation-ID retry has one effect and the
  original response; changed-payload reuse rejects. Check this across separate
  processes, all API adapters and supported storage backends.
- Validate stale worker publication with generation/lifecycle checks, tenant and
  system-owner isolation, and deployment/failover behavior before claiming the
  whole system is protected.

## 6. Running and recording evidence

Use Go from `go.mod` (currently 1.27.0), CGO for the existing SQLite test driver,
and a disposable PostgreSQL database with pgvector. No production DSN or API key.

```bash
docker run --rm --name fileio-race-pg \
  -e POSTGRES_PASSWORD=fileio-test-only -e POSTGRES_DB=fileio_test \
  -p 127.0.0.1:55432:5432 pgvector/pgvector:0.8.6-pg18-trixie
# In another terminal after PostgreSQL is ready:
export FILEIO_TEST_POSTGRES_DSN='postgres://postgres:fileio-test-only@127.0.0.1:55432/fileio_test?sslmode=disable'
CGO_ENABLED=1 go test -race -shuffle=on -count=3 -timeout=12m -v \
  ./internal/mcp/files -run '^TestFileIO'
go test -race -cover -timeout=15m ./...
make lint
```

Without the DSN, PostgreSQL subtests explicitly skip. A skipped PostgreSQL suite
is **not** production concurrency evidence. The dedicated PR workflow sets the
DSN, runs repeated histories plus repository race/coverage tests, and uploads
logs. It does not deploy. It runs vet and the pure-Go/system-owner gates, not the
complete `make lint` toolchain.

Authoring-environment status: Go formatting/parser checks completed; this
container has Go 1.23.2, no PostgreSQL, and dependency-network resolution is
unavailable. No local Go 1.27 test or complete lint success is claimed. Consult
the PR's actual workflow result and logs for execution evidence.
