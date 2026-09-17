# FileIO concurrent editing: audit and implemented version control

> **2026-09-17 decision:** External mutation preconditions are mandatory, including APPEND, and completed startup migrations are skipped using a durable checkpoint. The updated [FileIO versioning manual](../manual/fileio_versioning.md) is the implementation contract. Earlier execution evidence predates this follow-up patch.

Date: 2026-09-16. Audited base: `55c7e4c3acefb72797eccf6d37b0ad831815ad0c`.
Status: **version preconditions and integrity guards implemented in PR #45**.
The exact shipped contract, rollout requirements, examples, and test commands are
in [FileIO versioned editing](../manual/fileio_versioning.md). That manual is the
source of truth for the API changes, rather than the original proposal.

## Decision and scope

Retain the PostgreSQL project transaction lock. Add a non-reused file incarnation
plus a monotonically increasing live revision, compare it atomically with the
write, and return read content and its token from the same snapshot. Validate
UTF-8 inputs, overwrite/read boundaries, and complete write results.

External MCP/HTTP mutations now require conditions. Missing conditions reject
before reaching a backend; no compatibility bypass or blind APPEND is exposed.
Internal imperative storage operations remain available to memory/index/system
code and always advance revisions. Startup automatically applies the complete
FileIO migration in one transaction with its durable completion marker; completed
startups skip DDL, backfill and migration locks.

The original proposal also discussed durable idempotency keys, directory read
sets and asynchronous publisher fences. These remain separate follow-ups,
**not implemented guarantees of version CAS**.

## Original findings and regression disposition

The audit established that transaction serialization alone cannot protect a
client's earlier read/compute step. The original unsafe histories were executed
on SQLite and PostgreSQL; see [audit execution](https://github.com/Laisky/laisky-blog-graphql/actions/runs/35160556729).
This historical run is not evidence for the later implementation.

| Finding | Original behavior | Implemented regression expectation |
| --- | --- | --- |
| FILEIO-001 | A increments a JSON field; B saves an independent edit from its old copy and loses A's increment. | B's stale token conflicts without effects; re-read/recompute preserves both edits. |
| FILEIO-002 | A stale offset overwrites the first byte of a multibyte character, leaving invalid UTF-8. | Stale token conflicts; even an internal imperative write cannot split a code point or commit invalid UTF-8. |
| FILEIO-003 | An old byte offset changes `note` into `not1`; the JSON remains syntactically valid. | A stale edit conflicts before touching bytes. JSON syntax checks alone are not the oracle. |
| FILEIO-004 | Separate range reads assemble old prefix plus new suffix, a file generation that never existed. | Ranges pinned to the first token either share the generation or fail, including empty/EOF reads. |
| FILEIO-005 | Retrying APPEND duplicates the record. | Same-token conditional retry has no second effect but returns conflict, not replayed success. External blind appends reject; the internal primitive test is not a public-client contract. |
| FILEIO-006 | A stale save recreates a deleted or renamed path. | An old token cannot fall through to create-on-write. External requests cannot omit intent. |
| FILEIO-007 | A→B→A repeats a hash, and delete/recreate can repeat content/timestamps. | Same bytes have a new revision; recreated files have a new identity. |
| FILEIO-008 | JSON serialization replaces invalid stored bytes with U+FFFD. | Invalid writes are rejected; legacy corrupt live content produces an explicit read error. Successful wire content preserves Unicode bytes. |

Content size and SHA-256 can faithfully describe corrupted bytes. They remain
useful integrity metadata but do not prove that an edit used the correct base or
changed the intended business field. Revision checking also does not validate
an LLM's new business decision or automatically merge conflicting edits.

## Alternatives retained from the design review

These are project-specific design judgments supported by the
[primary-source reference index](../ref/20260916_fileio_concurrency_sources.md).

| Approach | Strength | Limitation / decision |
| --- | --- | --- |
| Existing transaction/advisory lock | Coordinates cooperating replicas, namespace checks, quota, history and outbox. | Keep it; it cannot detect a client's obsolete edit base. |
| Incarnation + monotonic revision + atomic condition | Detects stale content/lifecycle bases, recreation and mutation ABA. | Implemented and mandatory externally. Unrelated edits to the same file can conflict. |
| Content-hash condition | Detects changed bytes and can reuse existing hashing. | Valid for byte equality, but equal content after ABA/recreation is not the chosen mutation-generation contract. |
| Timestamps or historical snapshot IDs | Existing metadata. | Precision/repetition and prunable history do not define the live file's revision. Not selected. |
| SERIALIZABLE-only isolation | Protects a single database transaction. | The client read/compute is outside the write transaction; not a replacement for CAS. |
| Long-lived edit lock / distributed lease | Can reserve a cooperating editing session. | Requires expiry, crash handling and fencing; no additional lock service for this requirement. |
| Conditional patch / three-way merge | May retain independent edits against a known base; JSON Patch supports `test`. | Requires semantic/offset/array checks and conflict policy. Optional later layer. |
| CRDT / OT | Supports collaborative operation models. | New data/client/history model; convergence is not business correctness. Not justified for this change. |
| Durable operation ID + request fingerprint + stored response | Deduplicates retries of one logical request. | Complementary to versions. Not implemented; do not claim exactly-once or success replay. |

## Implementation boundaries

The database owns identity assignment and revision advancement. This covers
all SQL content/path/lifecycle writers and system namespaces without depending
on every call site remembering to increment a field. The transaction contains
condition checking, content/history/outbox effects, and the result token.
Counters cannot wrap.

MCP read/write/delete/rename, the native RAG/PageIndex adapters, internal
WriteWith/restore paths, and existing HTTP edit/restore endpoints participate.
A file token cannot authorize a directory deletion or move. Such public requests
reject rather than falling back to unconditional directory-wide mutations.
Conditional rename checks both names; the PageIndex atomic-delete path shares
the same pre-mutation validator as ordinary Delete. A non-transactional or
unrecognized conditional backend cannot silently ignore a supplied condition.

The tests use independent service instances and multi-connection pools with real
PostgreSQL locking, not a process mutex or one-connection imitation. They inspect
bytes, Unicode validity, JSON intent, same-snapshot size/hash, historical
preimages, outbox generations and revision side effects. The original positive
append, disjoint overwrite, mixed-mode, lifecycle, namespace, quota and held
transaction histories remain in the internal storage suite.

## Validation and remaining work

PR #45 records actual execution by commit. The authoring container's formatting,
parser and SQLite SQL smoke checks are not substitutes for the real Go and
PostgreSQL suite. PostgreSQL tests explicitly skip without a disposable test DSN;
skips must never be reported as production-concurrency PASS.

No new CI is part of this implementation. The standalone workflow introduced by
the initial audit is removed from the final PR; use existing repository checks
and the test commands in the manual.

Remaining boundaries are explicit: no durable operation-ID deduplication,
automatic merge, directory snapshot/read-set protocol, database recovery epoch
automation, or complete asynchronous worker fencing. Multi-process HTTP
transport, real response-loss fault injection and failover require additional
acceptance. New regression PASS means the asserted guarded behavior works, not
that these unimplemented capabilities have been established.
