# MCP FileIO and `file_search` Technical Architecture Manual

Last updated: 2026-02-21 (UTC)

## 1. Objective and Feasibility

This manual defines the production implementation plan for the MCP FileIO toolset and `file_search`, fully aligned with `docs/requirements/mcp_files.md`.

Feasibility is confirmed.

Why this is feasible in the current repository:

- MCP transport, tool registration, and per-request context injection already exist in `internal/mcp/server.go`.
- PostgreSQL-backed MCP services already exist (`ask_user`, `call_log`, `user_requests`, `rag`) and provide migration and service patterns.
- Background worker patterns already exist (`userrequests` retention worker) and can be reused for index/purge workers.
- Embedding client integration already exists in `internal/mcp/rag/openai_embedder.go`.
- Call auditing already exists (`recordToolInvocation` + `internal/mcp/calllog`), which can be extended with redaction.

## 2. Current System Analysis (What We Reuse)

### 2.1 MCP Server and Tool Wiring

- `internal/mcp/server.go` already provides:
  - tool registration
  - request-scoped logger injection
  - invocation auditing via `recordToolInvocation`
- `internal/mcp/settings.go` already supports feature flags under `settings.mcp.tools.*`.

### 2.2 Auth and Tenant Primitives

- `askuser.ParseAuthorizationContext` already computes:
  - raw token
  - `apikey_hash` (SHA-256)
  - user identity labels
- Current tools still parse auth individually.
- FileIO requires stronger normalization: parse once in MCP transport and pass trusted `AuthContext` to FileIO handlers only.

### 2.3 Database and Service Patterns

- Existing MCP services use `gorm` for writes/migrations and raw SQL for read-heavy queries.
- Existing code already enforces UTC timestamps.
- Existing tests already use `testify/require`.

### 2.4 Important Gaps vs PRD

- No FileIO tool package exists yet.
- No file/search schema exists.
- No advisory lock logic exists for per-project serialization.
- No outbox-based indexing worker exists.
- No call-log redaction for large/sensitive tool parameters exists.
- Current HTTP logging can leak request/response payload fragments and must be redacted for file content.

## 3. PRD-Aligned Functional Scope

The implementation must deliver all v1 capabilities below:

1. Durable project-scoped file storage with strict tenant isolation by `apikey_hash`.
2. Deterministic file operations:
   - `file_stat`
   - `file_read`
   - `file_write`
   - `file_delete`
   - `file_rename`
   - `file_list`
3. RAG retrieval over files:
   - `file_search`
4. No directory table; directories are implicit from file paths.
5. Eventual consistency for search with hard filtering to never return deleted files.
6. Stable machine-readable error codes for agent branching.

Non-goals (v1): exactly as PRD section 13.

### 3.1 Canonical Domain Types

Use these exact logical contracts:

```go
type FileType string

const (
    FileTypeFile      FileType = "FILE"
    FileTypeDirectory FileType = "DIRECTORY"
)

type WriteMode string

const (
    WriteModeAppend    WriteMode = "APPEND"
    WriteModeOverwrite WriteMode = "OVERWRITE"
    WriteModeTruncate  WriteMode = "TRUNCATE"
)

type FileEntry struct {
    Name      string
    Path      string
    Type      FileType
    Size      int64
    CreatedAt time.Time // zero for directories
    UpdatedAt time.Time
}

type ChunkEntry struct {
    FilePath           string
    FileSeekStartBytes int64 // inclusive
    FileSeekEndBytes   int64 // exclusive
    ChunkContent       string
    Score              float64
}
```

### 3.2 MCP Tool I/O Shape

Recommended tool-layer response payloads:

- `file_stat`: `{ exists, type, size, created_at, updated_at }`
- `file_read`: `{ content, content_encoding }`
- `file_write`: `{ bytes_written }`
- `file_delete`: `{ deleted_count }`
- `file_rename`: `{ moved_count }`
- `file_list`: `{ entries: FileEntry[], has_more }`
- `file_search`: `{ chunks: ChunkEntry[] }`

Error payload:

- `{ code, message, retryable }` with `is_error=true`.

### 3.3 Requirement Traceability (Critical Items)

| PRD requirement                   | Implementation anchor                                         |
| --------------------------------- | ------------------------------------------------------------- |
| tenant isolation by `apikey_hash` | trusted `AuthContext` + all SQL filters include `apikey_hash` |
| no directory table                | directory synthesized from file path prefixes                 |
| deterministic file ops            | explicit per-tool validators + mode/offset rules              |
| advisory lock serialization       | mutation transaction wrapper in `lock.go`                     |
| atomic rename/move semantics      | transactional path remap + dual outbox jobs per moved file    |
| outbox async indexing             | `mcp_file_index_jobs` + worker loop                           |
| eventual search consistency       | worker-based index visibility + freshness metrics             |
| deleted files never searchable    | search SQL joins active `mcp_files` only                      |
| machine-stable error codes        | typed error model in `errors.go`                              |

## 4. Target Architecture

### 4.1 High-Level Components

1. MCP transport and auth normalization
2. FileIO tool handlers (`internal/mcp/tools`)
3. File service core (`internal/mcp/files`)
4. PostgreSQL storage (`mcp_files`, chunk/index tables, index job outbox)
5. Async index workers (outbox consumer)
6. Search pipeline (semantic + lexical + rerank + fallback)
7. Audit and redacted logging

### 4.2 Runtime Data Flow

1. Client calls FileIO MCP tool with Authorization header.
2. MCP transport normalizes authorization into trusted `AuthContext`.
3. Tool validates input and calls `files.Service`.
4. For writes/deletes/renames:
   - acquire advisory transaction lock for `(apikey_hash, project)`
   - mutate `mcp_files`
   - enqueue outbox job in same transaction
   - for rename/move, enqueue `DELETE(old_path)` and `UPSERT(new_path)` per moved file
5. Background worker consumes outbox jobs and refreshes chunk/vector/BM25 rows.
6. `file_search` performs hybrid retrieval and optional rerank, then updates `last_served_at` only for returned chunks.

## 5. Package and File Design

To keep files maintainable (<600 lines preferred), split by concern.

### 5.1 New Backend Package

`internal/mcp/files/`

- `settings.go`
- `errors.go`
- `types.go`
- `models_files.go`
- `models_search.go`
- `path_validation.go`
- `content_validation.go`
- `lock.go`
- `migration.go`
- `service_stat_read.go`
- `service_write_delete.go`
- `service_rename.go`
- `service_list.go`
- `service_search.go`
- `index_jobs.go`
- `index_worker.go`
- `chunker.go`
- `rerank_client.go`
- `logging_redaction.go`
- `*_test.go` files per module

### 5.2 New MCP Tool Handlers

`internal/mcp/tools/`

- `file_stat.go`
- `file_read.go`
- `file_write.go`
- `file_delete.go`
- `file_rename.go`
- `file_list.go`
- `file_search.go`

Each follows existing tool pattern: `Definition()` + `Handle()`.

### 5.3 Required Integration Updates

- `internal/mcp/server.go`: register tools, add handlers, add file tools to `mcp_pipe` invoker switch.
- `internal/mcp/settings.go`: add feature flags for FileIO tools/search.
- `cmd/api.go`: initialize `files.Service`, workers, dependencies.
- `internal/web/server.go`: expose file tool flags in runtime config.
- `web/src/lib/runtime-config.ts` and nav/home/call-log UI lists: include one `file_io` entry.

### 5.4 Frontend UX (v1 Confirmed)

Do not build one page per FileIO tool in v1.

Implement one FileIO console entry:

- menu entry: `FileIO`
- target route: single FileIO page
- page behavior:
  - browse by `project`
  - render directory tree per selected project
  - click file to view content
  - provide action controls for core operations (stat/read/write/delete/rename/list/search) within this unified page

## 6. Authorization and Tenant Isolation

### 6.1 Trusted AuthContext

Add a shared context object (example):

```go
type AuthContext struct {
    APIKeyHash   string
    UserIdentity string // display only
}
```

Rules:

- FileIO authorization must use `AuthContext.APIKeyHash` only.
- `deriveUserIdentity(token)` remains display-only.
- FileIO tool handlers must not parse raw Authorization headers directly.
- `AuthContext` must be injected in transport once and then consumed by tools.

### 6.2 Isolation Surfaces (Mandatory)

`apikey_hash` must scope:

- all file queries/mutations
- all search queries
- unique constraints
- advisory lock key
- quota and rate-limit checks
- indexing jobs and workers

## 7. Data Model (PostgreSQL)

Implement PRD schema exactly, with idempotent migration SQL.

### 7.1 Files Table

```sql
CREATE TABLE IF NOT EXISTS mcp_files (
    id            BIGSERIAL PRIMARY KEY,
    apikey_hash   CHAR(64)      NOT NULL,
    project       VARCHAR(128)  NOT NULL,
    path          VARCHAR(1024) NOT NULL,
    content       BYTEA         NOT NULL,
    size          BIGINT        NOT NULL DEFAULT 0,
    created_at    TIMESTAMPTZ   NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ   NOT NULL DEFAULT now(),
    deleted       BOOLEAN       NOT NULL DEFAULT FALSE,
    deleted_at    TIMESTAMPTZ
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_mcp_files_active
ON mcp_files (apikey_hash, project, path)
WHERE deleted = FALSE;

CREATE INDEX IF NOT EXISTS idx_mcp_files_prefix
ON mcp_files (apikey_hash, project, path text_pattern_ops)
WHERE deleted = FALSE;

CREATE INDEX IF NOT EXISTS idx_mcp_files_deleted_at
ON mcp_files (deleted_at)
WHERE deleted = TRUE;
```

### 7.2 Search Tables

```sql
CREATE TABLE IF NOT EXISTS mcp_file_chunks (
    id              BIGSERIAL PRIMARY KEY,
    apikey_hash     CHAR(64)      NOT NULL,
    project         VARCHAR(128)  NOT NULL,
    file_path       VARCHAR(1024) NOT NULL,
    chunk_index     INT           NOT NULL,
    start_byte      BIGINT        NOT NULL,
    end_byte        BIGINT        NOT NULL,
    chunk_content   TEXT          NOT NULL,
    content_hash    CHAR(64)      NOT NULL,
    created_at      TIMESTAMPTZ   NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ   NOT NULL DEFAULT now(),
    last_served_at  TIMESTAMPTZ,
    UNIQUE (apikey_hash, project, file_path, chunk_index)
);

CREATE INDEX IF NOT EXISTS idx_mcp_file_chunks_prefix
ON mcp_file_chunks (apikey_hash, project, file_path text_pattern_ops);

CREATE TABLE IF NOT EXISTS mcp_file_chunk_embeddings (
    chunk_id        BIGINT        PRIMARY KEY REFERENCES mcp_file_chunks(id) ON DELETE CASCADE,
    embedding       VECTOR(1536)  NOT NULL,
    model           VARCHAR(128)  NOT NULL,
    created_at      TIMESTAMPTZ   NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ   NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS mcp_file_chunk_bm25 (
    chunk_id        BIGINT        PRIMARY KEY REFERENCES mcp_file_chunks(id) ON DELETE CASCADE,
    tokens          JSONB         NOT NULL,
    token_count     INT           NOT NULL,
    tokenizer       VARCHAR(64)   NOT NULL,
    created_at      TIMESTAMPTZ   NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ   NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS mcp_file_index_jobs (
    id              BIGSERIAL PRIMARY KEY,
    apikey_hash     CHAR(64)      NOT NULL,
    project         VARCHAR(128)  NOT NULL,
    file_path       VARCHAR(1024) NOT NULL,
    operation       VARCHAR(16)   NOT NULL, -- UPSERT / DELETE
    file_updated_at TIMESTAMPTZ,
    status          VARCHAR(16)   NOT NULL DEFAULT 'pending',
    retry_count     INT           NOT NULL DEFAULT 0,
    available_at    TIMESTAMPTZ   NOT NULL DEFAULT now(),
    created_at      TIMESTAMPTZ   NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ   NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_mcp_file_index_jobs_pending
ON mcp_file_index_jobs (status, available_at, id);
```

### 7.3 App-Level Constraints

- Enforce PRD path max length `<=512` in application validation (DB remains 1024).
- `size` must match `len(content)`.
- `start_byte < end_byte` and ranges must stay in file-size bounds.

## 8. Validation Rules

### 8.1 Project Validation

- required, length `1..128`
- recommended safe charset: `A-Z a-z 0-9 _-.`

### 8.2 Path Validation

- length `0..512`
- empty string means project root
- non-root path:
  - must start with `/`
  - must not end with `/`
  - no empty segment (`//`)
  - no `.` or `..` segments
  - no ASCII control chars
  - only `A-Z a-z 0-9 / _ - .`
  - no whitespace

Validation failures return `INVALID_PATH`.

### 8.3 Content Validation

- text-only
- `content_encoding` must be `utf-8` (default `utf-8`)
- request payload size, file size, and project quota limits enforced

Error mapping:

- payload/file overflow: `PAYLOAD_TOO_LARGE`
- project quota overflow: `QUOTA_EXCEEDED`

## 9. Tool Contracts and Behavior

All offsets are byte-based and follow `[start, end)`.

### 9.1 `file_stat(project, path)`

- `exists=false` is a normal success path.
- file row present and active => `FILE`.
- otherwise, directory exists if any active descendant matches `path + "/%"`.
- root (`path=""`) exists when project contains at least one active file.

Execution outline:

1. validate `project` and `path`.
2. query active file by exact `(apikey_hash, project, path)`.
3. if found, return file metadata.
4. otherwise query descendant existence:
   - root: `EXISTS active file in project`
   - non-root: `EXISTS active file with path LIKE path || '/%'`
5. if directory exists, return directory metadata (`created_at` zero).
6. else return `exists=false`.

### 9.2 `file_read(project, path, offset=0, length=-1)`

- path must resolve to active file, not directory.
- `offset > EOF` returns empty string.
- `length=-1` reads to EOF.
- `offset<0` or `length<-1` => `INVALID_OFFSET`.

Execution outline:

1. validate project/path/offset/length.
2. fetch active file content by exact path.
3. if not found:
   - if descendants exist => `IS_DIRECTORY`
   - else => `NOT_FOUND`
4. slice bytes by offset/length.
5. return UTF-8 content and `content_encoding="utf-8"`.

### 9.3 `file_write(project, path, content, content_encoding="utf-8", offset=0, mode=APPEND)`

Mode semantics:

- `APPEND`: ignore offset, write at EOF.
- `TRUNCATE`: requires `offset==0`, clear then write.
- `OVERWRITE`: replace bytes starting at offset without truncating remainder.

Offset semantics:

- `offset<0` => `INVALID_OFFSET`
- `OVERWRITE && offset>size` => `INVALID_OFFSET`
- `OVERWRITE && offset==size` is valid append-at-end

Path conflicts before write:

- descendant under `path + "/"` => `IS_DIRECTORY`
- parent segment that is an existing file => `NOT_DIRECTORY`

Creation:

- missing file created automatically
- missing parent directories are implicit

Execution outline:

1. validate project/path/content/encoding/mode/offset.
2. start transaction and acquire project advisory lock.
3. check path conflicts:
   - descendant exists under target path => `IS_DIRECTORY`
   - any parent segment is an active file => `NOT_DIRECTORY`
4. read current file row (if exists).
5. apply mode semantics to build new byte content.
6. enforce:
   - payload bytes
   - resulting file bytes
   - resulting project total bytes
7. upsert `mcp_files` row (`deleted=false`, update size/content/timestamps).
8. insert outbox `UPSERT` job in same transaction.
9. commit and return `bytes_written`.

### 9.4 `file_delete(project, path, recursive=false)`

- file path => soft-delete single file
- directory path:
  - `recursive=false` and has descendants => `NOT_EMPTY`
  - `recursive=true` => soft-delete descendants

Root handling:

- `("", recursive=false)` => `INVALID_PATH`
- `("", recursive=true)` => project wipe if enabled
- if disabled => `PERMISSION_DENIED`

Execution outline:

1. validate inputs and root-wipe policy.
2. start transaction and acquire project advisory lock.
3. resolve deletion target set:
   - file exact match
   - directory descendants by prefix
   - root wipe: all project files
4. enforce `NOT_EMPTY` for non-recursive directory delete.
5. soft-delete rows (`deleted=true`, `deleted_at=now`).
6. enqueue `DELETE` outbox jobs for affected paths.
7. commit and return deleted count.

### 9.5 `file_rename(project, from_path, to_path, overwrite=false)`

Rename/move semantics:

- supports file rename and directory subtree move.
- source path must be non-root and can resolve to:
  - exact active file
  - synthesized directory (has active descendants)
- destination path must be non-root and valid.
- `from_path == to_path` is a successful no-op with `moved_count=0`.
- destination parent segments must not resolve to active files (`NOT_DIRECTORY`).
- directory move must not target its own subtree (`INVALID_PATH`), for example:
  - `from_path=/docs`, `to_path=/docs/sub/new`
- destination conflicts:
  - `overwrite=false`: any active destination collision => `ALREADY_EXISTS`
  - `overwrite=true`: only single-file source may replace an existing file at exact destination path
  - directory move with destination collisions is rejected (`ALREADY_EXISTS`)

Path remap rule:

- for each source file path `src`, destination is:
  - `dst = to_path + strings.TrimPrefix(src, from_path)`

Execution outline:

1. validate `project`, `from_path`, `to_path`, and `overwrite`.
2. reject root source or root destination.
3. if `from_path == to_path`, return `moved_count=0`.
4. start transaction and acquire project advisory lock.
5. resolve source file set:
   - exact file match at `from_path`, or
   - descendant files under `from_path + "/%"`, else `NOT_FOUND`.
6. validate destination constraints and collision policy.
7. apply transactional path remap update on `mcp_files` with `updated_at=now`.
8. if overwrite-replace applies, soft-delete the replaced destination file first.
9. enqueue outbox jobs per moved file:
   - `DELETE` for `old_path`
   - `UPSERT` for `new_path`
10. commit and return `moved_count`.

### 9.6 `file_list(project, path="", depth=1, limit=256)`

- root path is `path=""`.
- MCP compatibility: `path="/"` is accepted by the `file_list` tool adapter and normalized to `""` before service validation.
- `depth=0`: metadata for path only
- `depth=1`: immediate children
- `depth>1`: recursive to given depth
- sorted by `path ASC`
- pagination not required in v1
- `limit` mandatory; `has_more=true` when truncated
- directories are synthesized; `created_at` zero for directories

Execution outline:

1. validate project/path/depth/limit.
2. for `depth=0`, return metadata for the path itself.
3. for `depth>=1`, scan active file rows under prefix ordered by path.
4. synthesize directory entries from path segments (no directory table).
5. deduplicate entries by path.
6. sort by `path ASC`, apply `limit`, compute `has_more`.

### 9.7 `file_search(project, query, path_prefix="", limit=5)`

- `query` must be non-empty after trim, else `INVALID_QUERY`.
- `path_prefix` is raw string-prefix filter (not directory-boundary filter).
- limit default 5, max 20.
- return only chunks from active files (never deleted).
- order by final score descending.
- eventual consistency accepted.

Execution outline:

1. validate project/query/path_prefix/limit.
2. embed query text.
3. fetch semantic candidates (`top_n_semantic`).
4. fetch lexical candidates (`top_n_lexical`).
5. merge and dedupe candidates.
6. rerank merged set via external rerank API.
7. on rerank failure, apply fused fallback score.
8. trim to `limit`, update `last_served_at` for returned chunk IDs only.
9. return `ChunkEntry[]`.

## 10. Concurrency and Consistency Design

### 10.1 Required Guarantee

Sequential consistency for mutations within `(apikey_hash, project)`, across all instances.

### 10.2 Advisory Lock

Each mutating transaction (`file_write`, `file_delete`, `file_rename`) must:

1. compute lock key from `apikey_hash + ":" + project`
2. acquire transaction-scoped advisory lock (`pg_advisory_xact_lock` equivalent behavior)
3. fail with `RESOURCE_BUSY` when timeout exceeds `settings.mcp.files.lock_timeout_ms`

Implementation detail:

- use `pg_try_advisory_xact_lock` polling loop with bounded timeout.

### 10.3 Read Visibility

- `file_read` and `file_list` see committed writes only.
- `file_search` is eventual due to async indexing.
- PRD freshness SLO: `P95 <= 30s` mutation-commit to searchable visibility.

## 11. Indexing Pipeline (Outbox + Worker)

### 11.1 Producer Side (Write/Delete/Rename Transaction)

In the same DB transaction as file mutation:

1. apply file mutation
2. insert outbox row (`UPSERT` or `DELETE`)
   - rename/move emits `DELETE(old_path)` + `UPSERT(new_path)` per moved file
3. commit

Outbox insertion must be idempotent-safe for repeated calls and retries.

### 11.2 Worker Side

Workers run continuously:

1. claim pending jobs in batches via `FOR UPDATE SKIP LOCKED`
2. deduplicate by latest file state for `(apikey_hash, project, file_path)`
3. process each job:
   - `UPSERT`: load encrypted credential envelope from Redis, decrypt in memory, re-read active file, chunk content, regenerate embeddings/tokens, upsert index rows, and delete envelope immediately after external calls complete
   - `DELETE`: delete chunk rows by `(apikey_hash, project, file_path)` cascade removes embeddings/BM25
4. mark job done or reschedule with backoff on failure

Retry behavior uses:

- `settings.mcp.files.index.retry_max`
- `settings.mcp.files.index.retry_backoff_ms`

Credential behavior:

- worker must never persist or log plaintext API keys.
- if credential envelope is missing/expired, worker must retry later.
- worker must not fall back to platform credentials.

Job status lifecycle:

- `pending` -> `processing` -> `done`
- `processing` -> `pending` (retry with backoff)
- `processing` -> `failed` (retry limit reached)

### 11.3 Chunking Requirements

Chunker must preserve stable byte offsets:

- each chunk stores `start_byte`, `end_byte`, and exact `chunk_content`
- byte ranges must slice original stored file content directly
- offsets must remain compatible with `file_read`

### 11.4 `last_served_at`

- update only chunks included in final `file_search` response
- never update candidate-only chunks

## 12. Search and Rerank Flow

Given `file_search(project, query, path_prefix, limit)`:

1. semantic candidate query (`top_n_semantic`, default 30)
2. lexical candidate query (`top_n_lexical`, default 30)
   - v1 allows `tsvector`-based lexical retrieval as interim implementation.
   - fused ranking must still distinguish and combine semantic and lexical signals explicitly.
3. merge/deduplicate candidates
4. call rerank API (Cohere-compatible, default model `rerank-v3.5`)
5. return top `limit` results as `ChunkEntry`

Rerank fallback (mandatory):

- on timeout/failure, compute fused score:
  - `semantic_weight * normalized_semantic + lexical_weight * normalized_lexical`
- still apply:
  - tenant/project filter
  - `path_prefix` filter
  - active file join filter (`mcp_files.deleted=FALSE`)

## 13. Error Model

Machine-stable codes:

- `NOT_FOUND`
- `ALREADY_EXISTS`
- `IS_DIRECTORY`
- `NOT_DIRECTORY`
- `INVALID_PATH`
- `INVALID_OFFSET`
- `INVALID_QUERY`
- `NOT_EMPTY`
- `PERMISSION_DENIED`
- `PAYLOAD_TOO_LARGE`
- `QUOTA_EXCEEDED`
- `RATE_LIMITED`
- `RESOURCE_BUSY`
- `SEARCH_BACKEND_ERROR`

Tool error payload format (recommended):

```json
{
  "code": "INVALID_PATH",
  "message": "path must start with '/'",
  "retryable": false
}
```

`is_error` must be true for these responses.

## 14. Logging, Redaction, and Auditing

### 14.1 Never Log Full File Content

For file operations, logs may include:

- content length
- optional content SHA-256
- preview up to `N` bytes (default 256), with `truncated=true`

### 14.2 MCP Hook and HTTP Log Redaction

Redact sensitive/large fields in:

- request/response hook logs (`newMCPHooks`)
- HTTP request/response body logs (`withHTTPLogging`)
- call-log persisted `parameters`

At minimum redact:

- `content` argument in `file_write`
- any response fields containing full file content

### 14.3 Error Processing Rule

Each error should be handled once (return or log, not both) and always wrapped with stack/context in service layer.

## 15. Configuration

All FileIO settings must come from service configuration files (for example `settings.yml`).
Environment variables are not a supported configuration source for FileIO.

Billing strategy (v1 confirmed):

- FileIO tool calls are zero-cost in v1.
- do not call external billing checker for FileIO tools.
- keep call-log records for FileIO invocations with `cost=0`.

Core FileIO:

- `settings.mcp.files.allow_root_wipe` (default `false`)
- `settings.mcp.files.max_payload_bytes`
- `settings.mcp.files.max_file_bytes`
- `settings.mcp.files.max_project_bytes`
- `settings.mcp.files.list_limit_default` (default `256`)
- `settings.mcp.files.list_limit_max`
- `settings.mcp.files.lock_timeout_ms`
- `settings.mcp.files.delete_retention_days`

Search/index:

- `settings.mcp.files.search.enabled` (default `true`)
- `settings.mcp.files.search.limit_default` (default `5`)
- `settings.mcp.files.search.limit_max` (default `20`)
- `settings.mcp.files.search.vector_candidates` (default `30`)
- `settings.mcp.files.search.bm25_candidates` (default `30`)
- `settings.mcp.files.search.rerank.model` (default `rerank-v3.5`)
- `settings.mcp.files.search.rerank.endpoint` (default `https://oneapi.laisky.com/v1/rerank`)
- `settings.mcp.files.search.rerank.timeout_ms`
- `settings.mcp.files.search.fallback.semantic_weight`
- `settings.mcp.files.search.fallback.lexical_weight`
- `settings.mcp.files.index.workers`
- `settings.mcp.files.index.batch_size`
- `settings.mcp.files.index.retry_max`
- `settings.mcp.files.index.retry_backoff_ms`
- `settings.mcp.files.index.slo_p95_seconds` (default `30`)

Async credential handoff:

- `settings.mcp.files.security.encryption_keks` (required; map of `kek_id -> secret`, longest ID used for new encryption)
- `settings.mcp.files.security.credential_cache_prefix` (default `mcp:files:cred`)
- `settings.mcp.files.security.credential_cache_ttl_seconds` (default `300`)

Credential-source rule (v1 confirmed):

- remove `MCP_FILES_MODEL_KEY_SOURCE` from implementation and docs.
- all external model/rerank requests use the caller's API key.

## 16. Implementation Plan (Executable by Engineering Team)

### Phase 1: Foundation

1. Create `internal/mcp/files` package skeleton and settings/error modules.
2. Implement migrations for all FileIO/search/outbox tables and indexes.
3. Add tool feature flags in `internal/mcp/settings.go`.
4. Add trusted `AuthContext` injection in MCP transport context function.

### Phase 2: Core File Operations

1. Implement validators for project/path/content/offset.
2. Implement advisory lock helper and mutation transaction wrapper.
3. Implement `file_stat`, `file_read`, `file_write`, `file_delete`, `file_rename`, `file_list`.
4. Implement PRD-compliant error mapping and JSON tool errors.

### Phase 3: Search and Worker

1. Implement outbox producer in write/delete/rename code paths.
2. Implement credential-envelope service (Redis TTL cache + KMS encrypt/decrypt).
3. Implement index workers, retry/backoff, dedupe, and retention purge.
4. Implement chunker with stable byte offsets.
5. Implement semantic + lexical retrieval and rerank + fallback.
6. Implement `last_served_at` updates for final results only.

### Phase 4: Integration and UX

1. Register all tools in `internal/mcp/server.go`.
2. Add file tools to `mcp_pipe` invoker switch.
3. Extend runtime tool config and frontend tool visibility.
4. Extend call-log tool filters for new file tools.

### Phase 5: Hardening

1. Add full redaction in HTTP/hook/audit logging.
2. Add metrics and SLO dashboards.
3. Run load and concurrency tests for lock behavior and indexing freshness.

## 17. Testing Strategy

### 17.1 Unit Tests

Use `github.com/stretchr/testify/require` throughout.

- path/project/content validators
- offset and write-mode semantics
- directory conflict rules
- error-code mapping
- redaction helpers
- lock timeout behavior (sqlmock)

### 17.2 Service-Level Tests

- write/read/list/delete/rename scenarios across nested paths
- file rename and directory move conflict cases (`ALREADY_EXISTS`, `NOT_DIRECTORY`, subtree loops)
- root wipe permission switch
- quota enforcement cases
- tenant isolation (`apikey_hash` separation)
- project-level sequential consistency under concurrent writers

### 17.3 Worker/Search Tests

- outbox enqueue on write/delete/rename
- worker dedupe and retry/backoff
- credential envelope encrypt/decrypt round-trip and immediate delete after use
- missing/expired envelope triggers retry and never platform-key fallback
- index freshness checks (commit-to-search delay)
- rerank success/failure fallback correctness
- guarantee that deleted files never appear in search
- `last_served_at` update only for final returned chunks

### 17.4 Integration Tests

Prefer PostgreSQL integration tests for:

- advisory lock semantics
- prefix indexes
- vector column behavior
- outbox `FOR UPDATE SKIP LOCKED` claim flow

## 18. Operational Requirements

### 18.1 Metrics

Expose at least:

- `mcp_files_mutation_total{tool, status}`
- `mcp_files_lock_wait_ms`
- `mcp_files_index_queue_depth`
- `mcp_files_index_job_latency_ms`
- `mcp_files_search_latency_ms`
- `mcp_files_search_rerank_fail_total`
- `mcp_files_index_visibility_lag_ms` (for freshness SLO)

### 18.2 Background Jobs

- index worker pool
- deleted-file retention purge worker
- stale/terminal index-job cleanup worker

### 18.3 Alerting

- indexing lag P95 > configured SLO
- repeated rerank backend errors
- lock timeout surge (`RESOURCE_BUSY`)

## 19. Confirmed Decisions

1. Billing: FileIO tools are zero-cost in v1.
2. Frontend: do not build one page per tool; use one FileIO entry with project-based tree browsing and file-content view.
3. Credential source: remove `MCP_FILES_MODEL_KEY_SOURCE`; all external model/rerank calls use user API key.
4. Lexical retrieval: v1 may use `tsvector` transitional implementation, but final ranking must remain semantic + lexical fused results.

## 20. Async Credential Handoff Rule

For async index workers:

- never persist user raw API keys in plaintext storage.
- store a credential envelope in Redis with short TTL (for example 300s), keyed by job-scoped reference.
- envelope must contain encrypted data only (`kek_id`, `dek_id`, `ciphertext`, `aad`, `expires_at`).
- initialize KMS with `github.com/Laisky/go-utils/v6/crypto/kms/mem` using all entries in `settings.mcp.files.security.encryption_keks`.
- construct KMS by hashing each configured KEK secret and passing `map[uint16][]byte` into `mem.New(...)`.
- producer encryption call sequence:
  - `encryptedData, err := kmsClient.Encrypt(ctx, []byte(apiKey), aad)`
  - `payload, err := encryptedData.MarshalToString()`
  - write payload to Redis with TTL
- each envelope uses a unique `dek_id` generated by KMS; DEK is derived from `(kek_id, dek_id)` internally.
- worker decryption call sequence:
  - `var encryptedData kms.EncryptedData`
  - `encryptedData.UnmarshalFromString(payload)`
  - `plaintext, err := kmsClient.Decrypt(ctx, &encryptedData, aad)`
- worker decrypts in memory, uses caller API key for external requests, and deletes Redis envelope immediately after use.
- if transient credentials are unavailable, the worker must retry later without falling back to platform credentials.
- all decrypt/encrypt failures must be logged without sensitive payload fields.

## 21. Definition of Done Checklist

The implementation is done only when all are true:

1. All seven tools are available and PRD behavior matches exactly.
2. Tenant isolation is enforced by `apikey_hash` in every read/write/search/index path.
3. Mutation paths use project-level advisory transaction locks with timeout -> `RESOURCE_BUSY`.
4. Outbox worker indexing is operational with retries and dedupe.
5. `file_search` never returns deleted files and supports rerank fallback.
6. Error codes are machine-stable and used consistently.
7. Logs and call audits redact file content fields.
8. Config keys from PRD are implemented in service config with safe defaults.
9. Unit + integration tests cover core behavior and regressions.
10. Freshness SLO instrumentation is available for runtime verification.
