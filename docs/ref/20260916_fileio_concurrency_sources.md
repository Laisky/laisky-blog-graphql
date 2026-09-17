# FileIO concurrency: primary-source reference index

Checked: 2026-09-16. These are established mechanisms verified against current
primary documentation, not a claim that they were introduced in 2026.
Repository-specific decisions are in
[the behavioral audit and proposal](../proposals/20260916_fileio_concurrency_control.md).

| Ref | Primary source | Relevant fact and application |
| --- | --- | --- |
| R1 | [PostgreSQL: transaction isolation](https://www.postgresql.org/docs/current/transaction-iso.html) | READ COMMITTED statement snapshots; conditional UPDATE rechecks; serializable failures require transaction retry. Sequences are not rolled back. Separate client RPCs are not automatically one transaction. |
| R2 | [PostgreSQL: explicit/advisory locking](https://www.postgresql.org/docs/current/explicit-locking.html#ADVISORY-LOCKS) | Transaction-level advisory locks last through commit/rollback and require application cooperation. Retain the existing project's database lock for namespace/quota invariants. |
| R3 | [RFC 9110: If-Match](https://www.rfc-editor.org/rfc/rfc9110.html#name-if-match) and [If-None-Match](https://www.rfc-editor.org/rfc/rfc9110.html#name-if-none-match) | Strong conditional validation prevents lost updates; create-only checks protect competing creators. An ETag is a validator, not necessarily a hash. |
| R4 | [GCS request preconditions](https://docs.cloud.google.com/storage/docs/request-preconditions) | Generation matches condition mutations; generation-match zero is a special create-only condition. Failed preconditions return 412. Borrow the principle, not GCS-specific wire syntax. |
| R5 | [Go data race detector](https://go.dev/doc/articles/race_detector) | Dynamic detection of executed Go memory races. Database/client history invariants need their own behavioral assertions. |
| R6 | [Go encoding/json](https://pkg.go.dev/encoding/json#Marshal) and [unicode/utf8](https://pkg.go.dev/unicode/utf8) | The encoding/json string path substitutes invalid UTF-8 bytes with U+FFFD. Validating stored/request/result bytes is different from checking an encoding label. |
| R7 | [SQLite isolation](https://sqlite.org/isolation.html) | SQLite serializes writers and can report busy/snapshot conflicts. SQLite tests cannot establish PostgreSQL advisory-lock behavior. |
| R8 | [AWS Builders' Library: making retries safe with idempotent APIs](https://aws.amazon.com/builders-library/making-retries-safe-with-idempotent-APIs/) | Caller operation identity, same-ID/different-intent handling, atomic recording of token and side effects, and late retries. A content revision and an operation identity solve different problems. |
| R9 | [RFC 6902 JSON Patch](https://www.rfc-editor.org/rfc/rfc6902.html) | The `test` operation checks a value before later patch operations. Conditional patches need atomic execution and application validation; patches alone do not resolve stale-base conflicts. |
| R10 | [Automerge conflict behavior](https://automerge.org/docs/reference/documents/conflicts/) | Concurrent updates can retain conflicting values. Replication convergence is not proof of application-level semantic correctness. CRDT adoption is a product/data-model change, not a drop-in file-write lock. |
| R11 | [Kubernetes API concepts: resource versions](https://kubernetes.io/docs/reference/using-api/api-concepts/#resource-versions) | Updates can carry resourceVersion to detect lost updates; stale updates receive 409 Conflict. Use server-issued version tokens, not wall-clock assumptions. |
| R12 | [pgvector installation and Docker images](https://github.com/pgvector/pgvector#docker) | Upstream documents PostgreSQL 18 images. CI uses `pgvector/pgvector:0.8.6-pg18-trixie` in a disposable service, required because FileIO migrations create vector-backed tables. |

## Repository evidence map

Inspected immutable base: `55c7e4c3acefb72797eccf6d37b0ad831815ad0c`.

| File | Evidence |
| --- | --- |
| `internal/mcp/files/lock.go` | Transaction wrapper; project advisory-lock key/polling; non-PostgreSQL bypass. |
| `internal/mcp/files/service_write_delete.go` | Whole-byte merge, content/size/hash update, snapshot and outbox transaction; unconditional creation. |
| `internal/mcp/files/service_stat_read.go` | One-row read followed by byte slicing; no returned current revision. |
| `internal/mcp/files/service_versions.go` | Prior-content snapshots, retention and restore; not a live compare-and-swap protocol. |
| `internal/mcp/files/service_rename.go` | Transactional path/history remap and rename outbox jobs. |
| `internal/mcp/tools/file_write.go`, `file_read.go`, `file_tool_helpers.go` | Real MCP argument normalization, auth context, service invocation and JSON responses. |
| `internal/mcp/files/migration.go` | Existing schema, owner scoping, content hashes; no live mutation revision. |
| `docs/requirements/mcp_files.md` | Current byte-offset, create-on-write and UTF-8 contract. |

The new tests are executable evidence candidates. Test names and source analysis
must not be substituted for a recorded run. Keep CI logs with the tested commit
and distinguish characterization PASS, safety-invariant PASS, SKIP and failure.
