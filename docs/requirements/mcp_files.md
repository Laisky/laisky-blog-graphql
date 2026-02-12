# MCP FileIO Tool PRD

## 1. Purpose

This PRD defines a POSIX-aligned, agent-safe FileIO MCP tool that provides:

- shared, durable, project-scoped file storage across agents/clients
- deterministic file operations (`file_stat`, `file_read`, `file_write`, `file_delete`, `file_list`)
- RAG-based project search (`file_search`) over indexed file chunks

v1 delivery decisions:

- FileIO tools are zero-cost in v1 (no external billing check for FileIO tool calls).
- Web console uses one unified `FileIO` entry and page, not one page per file tool.
- All external model/rerank requests use caller credentials.
- Lexical retrieval may use a `tsvector` transitional implementation in v1, while still producing fused semantic + lexical final ranking.

The design intentionally excludes kernel-level complexity (file descriptors, permissions, inode-level metadata, and mmap).

## 2. Core Model

- Each `project` is an isolated filesystem root under one tenant.
- The project root (`path=""`) always exists as an implicit directory.
- Directories are implicit and derived from file paths.
- There is no physical directory table.
- `path` is the full file identity.
- Empty directories are not persisted.
- File search uses a separate, eventually consistent index derived from active files.

## 3. Tenant and Authorization Model

- Tenant isolation is enforced strictly by `apikey_hash`.
- `apikey_hash` must be used for all:
  - file read/write/delete filters
  - search index filters
  - unique constraints
  - lock scope
  - quota/rate-limit scope
- `deriveUserIdentity(token)` is display-only for logs and audit labels.
- `deriveUserIdentity(token)` must not be used for authorization.
- FileIO must not parse raw `Authorization` headers.
- MCP server normalizes authorization into a trusted `AuthContext` before tool invocation.
- FileIO handlers consume `AuthContext` only.

## 4. Path Rules

All paths are relative to project root.

- `project` length: `1..128`
- `path` length: `0..512`
- Empty path `""` means project root.
- `/` is the only separator.
- Path must start with `/`.
- Path must not end with `/` (except empty root path).
- Empty segments are forbidden (`a//b` is invalid).
- `.` and `..` segments are forbidden.
- ASCII control characters are forbidden.
- Path input must be a-z, A-Z, 0-9, and `/_-.` with no whitespace.
- Parent directories are auto-created on write.

Validation failures return `INVALID_PATH`.

## 5. Content Model and Limits

- FileIO stores text content only.
- `content_encoding` is required to be `utf-8` (default `utf-8`).
- Native binary payloads are not supported.
- Clients needing binary must encode to text (for example Base64) before writing.
- The service must enforce:
  - max payload size per request
  - max single file size
  - max bytes per project
- All limits are configurable in the service configuration file with safe defaults.
- No extra large-file indexing skip strategy is required, because upload size is already bounded by file-size limits.

Violations return `PAYLOAD_TOO_LARGE` or `QUOTA_EXCEEDED`.

## 6. API Surface

### 6.1 Types

```go
enum FileType {
    FILE,
    DIRECTORY,
}

enum WriteMode {
    APPEND,
    OVERWRITE,
    TRUNCATE,
}

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
    FileSeekStartBytes int64 // byte offset, inclusive
    FileSeekEndBytes   int64 // byte offset, exclusive
    ChunkContent       string
    Score              float64
}
```

`ChunkEntry` offsets must be byte offsets compatible with `file_read`.
Offsets use `[start, end)` and `end` may equal file size.

### 6.2 `file_stat`

```go
file_stat(project string, path string) (
    exists bool,
    typ FileType,
    size int64,
    created_at time.Time,
    updated_at time.Time,
    err error,
)
```

- `exists=false` is not an error.
- A directory exists if it has at least one descendant file.

### 6.3 `file_read`

```go
file_read(
    project string,
    path string,
    offset int64 = 0,
    length int64 = -1,
) (
    content string,
    content_encoding string, // always "utf-8"
    err error,
)
```

- `path` must resolve to a file.
- `offset > EOF` returns empty content.
- `length = -1` reads to EOF.

### 6.4 `file_write`

```go
file_write(
    project string,
    path string,
    content string,
    content_encoding string = "utf-8",
    offset int64 = 0,
    mode WriteMode = APPEND,
) (
    bytes_written int64,
    err error,
)
```

Write mode semantics:

- `APPEND`: offset ignored, always writes at EOF.
- `TRUNCATE`: offset must be `0`, file is cleared before writing.
- `OVERWRITE`: offset is respected without truncation.

Offset rules:

- `offset < 0` is invalid.
- `OVERWRITE` with `offset > size` returns `INVALID_OFFSET`.
- `OVERWRITE` with `offset == size` is valid and behaves like append-at-end.

Path conflict rules before write:

- If any descendant exists under `path + "/"`, writing file at `path` fails with `IS_DIRECTORY`.
- If any parent segment is an existing file, write fails with `NOT_DIRECTORY`.

Creation rules:

- Missing file is created.
- Missing parent directories are implicitly created.

### 6.5 `file_delete`

```go
file_delete(
    project string,
    path string,
    recursive bool = false,
) (err error)
```

- File path: remove the file.
- Directory path:
  - `recursive=false`: return `NOT_EMPTY` when descendants exist.
  - `recursive=true`: remove all descendants.

Root delete behavior:

- `file_delete(project, "", recursive=false)` returns `PERMISSION_DENIED`.
- `file_delete(project, "", recursive=true)` also returns `PERMISSION_DENIED`.
- Root directory is immutable and must never be deleted.

### 6.6 `file_list`

```go
file_list(
    project string,
    path string = "",
    depth int = 1,
    limit int = 256,
) (
    entries []FileEntry,
    has_more bool,
    err error,
)
```

Depth semantics:

- `depth = 0`: metadata for `path` only.
- `depth = 1`: immediate children.
- `depth > 1`: recursive to the requested depth.

List semantics:

- `path=""` is the canonical root path.
- MCP compatibility: `file_list` also accepts `path="/"` and normalizes it to `""` in the tool layer.
- Results must be sorted by `path ASC`.
- Pagination is not required in v1.
- `limit` is mandatory for response-size control.
- `has_more=true` indicates truncation by limit.

### 6.7 `file_search`

```go
file_search(
    project string,
    query string,
    path_prefix string = "",
    limit int = 5,
) (
    chunks []ChunkEntry,
    err error,
)
```

Parameter semantics:

- `project`: target project namespace.
- `query`: search query string, must be non-empty after trim.
- `path_prefix`: optional raw string prefix filter on file path.
- `limit`: max returned chunk entries, default `5`, max `20`.

Search behavior:

- `path_prefix` uses string-prefix semantics, not directory-boundary semantics.
- Results are ordered by final score descending.
- The implementation must return only active files (`mcp_files.deleted = FALSE`) and must never return deleted files.
- Search results are eventually consistent with file writes and updates.

## 7. Search Indexing and Retrieval Pipeline

### 7.1 Indexing Trigger and Worker Model

- `file_write` and `file_delete` enqueue index jobs (outbox pattern).
- Index build/update/delete is performed asynchronously by workers.
- Jobs must be idempotent and deduplicated by latest file state.
- Workers read active file content from `mcp_files`, split into chunks, and maintain embeddings + BM25 index rows.
- Indexing is tenant/project/path scoped by `(apikey_hash, project, path)`.
- Async workers must use caller credentials only through encrypted short-lived handoff data.
- `UPSERT` jobs that call external model APIs must carry a credential-envelope reference key (not plaintext API key).
- The credential envelope is stored in Redis with short TTL and deleted immediately after worker use.

### 7.2 Chunking Rules

- Chunk boundaries must preserve stable byte offsets against stored file content.
- Each chunk persists:
  - source file path
  - `[start_byte, end_byte)` offsets
  - chunk text payload
  - model/tokenizer metadata

### 7.3 Retrieval and Rerank Flow

For `file_search(project, query, path_prefix, limit)`:

1. Get semantic candidates (embedding) `top_n_semantic`.
2. Get lexical candidates (`BM25` target; `tsvector` transitional mode allowed in v1) `top_n_lexical`.
3. Merge and deduplicate candidate set.
4. Send merged candidates to rerank API (Cohere-compatible model, default `rerank-v3.5`).
5. Return top `limit` results as `ChunkEntry`.

Default candidate counts are `30 + 30`, configurable in the service configuration file.

### 7.4 Rerank Failure Fallback

- If rerank request times out or fails, service must degrade gracefully.
- Fallback strategy is direct fused scoring from semantic + lexical scores.
- Fallback must still honor tenant isolation, `path_prefix`, and deletion filtering.
- Transitional lexical mode must still preserve explicit semantic-vs-lexical score components before fusion.

### 7.5 Final-Result Timestamp Tracking

- Each indexed chunk stores `last_served_at`.
- `last_served_at` is updated only for chunks that are actually returned to the user as final `file_search` results.
- Candidate retrieval without final return must not update `last_served_at`.

## 8. Consistency, Freshness, and Concurrency

- Sequential consistency is required within `(apikey_hash, project)` for file mutation operations.
- Cross-instance consistency is required.
- All mutating file operations (`file_write`, `file_delete`) must obtain:
  - `pg_advisory_xact_lock(hash(apikey_hash, project))`
- Lock is transaction-scoped.
- Lock acquisition timeout is configurable.
- Timeout/failure to acquire lock returns `RESOURCE_BUSY`.
- `file_read` and `file_list` observe committed writes only.
- `file_search` is eventually consistent for indexing visibility.
- Freshness SLO: `P95 <= 30s` from successful file mutation commit to searchable index visibility.
- Even under eventual consistency, deleted files must never appear in `file_search` results due to active-file filtering at query time.

## 9. Error Codes

Errors must be machine-stable for agent branching logic.

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

## 10. Storage (PostgreSQL)

### 10.1 Files Table

```sql
CREATE TABLE mcp_files (
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

CREATE UNIQUE INDEX uq_mcp_files_active
ON mcp_files (apikey_hash, project, path)
WHERE deleted = FALSE;

CREATE INDEX idx_mcp_files_prefix
ON mcp_files (apikey_hash, project, path text_pattern_ops)
WHERE deleted = FALSE;

CREATE INDEX idx_mcp_files_deleted_at
ON mcp_files (deleted_at)
WHERE deleted = TRUE;
```

### 10.2 Search Index Tables

```sql
CREATE TABLE mcp_file_chunks (
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

CREATE INDEX idx_mcp_file_chunks_prefix
ON mcp_file_chunks (apikey_hash, project, file_path text_pattern_ops);

CREATE TABLE mcp_file_chunk_embeddings (
    chunk_id        BIGINT        PRIMARY KEY REFERENCES mcp_file_chunks(id) ON DELETE CASCADE,
    embedding       VECTOR(1536)  NOT NULL,
    model           VARCHAR(128)  NOT NULL,
    created_at      TIMESTAMPTZ   NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ   NOT NULL DEFAULT now()
);

CREATE TABLE mcp_file_chunk_bm25 (
    chunk_id        BIGINT        PRIMARY KEY REFERENCES mcp_file_chunks(id) ON DELETE CASCADE,
    tokens          JSONB         NOT NULL,
    token_count     INT           NOT NULL,
    tokenizer       VARCHAR(64)   NOT NULL,
    created_at      TIMESTAMPTZ   NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ   NOT NULL DEFAULT now()
);

CREATE TABLE mcp_file_index_jobs (
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

CREATE INDEX idx_mcp_file_index_jobs_pending
ON mcp_file_index_jobs (status, available_at, id);
```

Transitional lexical storage note (v1):

- When v1 uses `tsvector` transitional lexical retrieval, implementation may index lexical content directly from `mcp_file_chunks.chunk_content` with a PostgreSQL GIN `tsvector` index.
- In that mode, `mcp_file_chunk_bm25` can be treated as optional/deferred, but output ranking must still be fused semantic + lexical ranking.

Storage rules:

- `path` is the full file identity.
- There is no directory row/entity.
- Soft delete is for short-term recovery only, not version history.
- A background job purges soft-deleted rows after `retention_days`.

## 11. Logging and Redaction

- Full file content must not be logged.
- Search queries may be logged in trimmed form.
- Log only:
  - content length
  - optional content hash
  - optional preview up to `N` bytes (default `256`) with `truncated=true`
- MCP tool parameter logging must support field-level redaction for `content`.

## 12. Configuration (Service Config File)

All FileIO settings must be loaded from the service configuration file.
Environment variables are not a supported configuration source for FileIO in v1.

### 12.1 Core FileIO Keys

- `settings.mcp.files.allow_root_wipe` (default: `false`)
- `settings.mcp.files.max_payload_bytes`
- `settings.mcp.files.max_file_bytes`
- `settings.mcp.files.max_project_bytes`
- `settings.mcp.files.list_limit_default` (default: `256`)
- `settings.mcp.files.list_limit_max`
- `settings.mcp.files.lock_timeout_ms`
- `settings.mcp.files.delete_retention_days`

### 12.2 Search and Indexing Keys

- `settings.mcp.files.search.enabled` (default: `true`)
- `settings.mcp.files.search.limit_default` (default: `5`)
- `settings.mcp.files.search.limit_max` (default: `20`)
- `settings.mcp.files.search.vector_candidates` (default: `30`)
- `settings.mcp.files.search.bm25_candidates` (default: `30`)
- `settings.mcp.files.search.rerank.model` (default: `rerank-v3.5`)
- `settings.mcp.files.search.rerank.endpoint` (default: `https://oneapi.laisky.com/v1/rerank`)
- `settings.mcp.files.search.rerank.timeout_ms`
- `settings.mcp.files.search.fallback.semantic_weight`
- `settings.mcp.files.search.fallback.lexical_weight`
- `settings.mcp.files.index.workers`
- `settings.mcp.files.index.batch_size`
- `settings.mcp.files.index.retry_max`
- `settings.mcp.files.index.retry_backoff_ms`
- `settings.mcp.files.index.slo_p95_seconds` (default: `30`)

### 12.3 Async Credential Handoff and Encryption Keys

Credential handling requirements:

- Async indexing must never persist raw user API keys in plaintext.
- Embeddings/rerank requests must always use the caller's API key.
- Platform key fallback is forbidden.

Required config keys:

- `settings.mcp.files.security.encryption_keks` (required; map of `kek_id -> secret`)
- `settings.mcp.files.security.credential_cache_prefix` (default: `mcp:files:cred`)
- `settings.mcp.files.security.credential_cache_ttl_seconds` (default: `300`)

Implementation requirements:

- Initialize in-memory KMS using `github.com/Laisky/go-utils/v6/crypto/kms/mem`.
- Build KMS by hashing each `encryption_keks` secret and passing all KEKs to `mem.New(map[uint16][]byte{...})`.
- For each credential envelope, call `kmsClient.Encrypt(ctx, plaintextAPIKey, aad)`:
  - encryption output includes `kek_id`, unique `dek_id`, and `ciphertext`
  - serialize with `EncryptedData.MarshalToString()` before Redis write
- Worker reads serialized envelope and calls:
  - `EncryptedData.UnmarshalFromString(...)`
  - `kmsClient.Decrypt(ctx, encryptedData, aad)`
- Store only encrypted envelope in Redis.
- Worker flow:
  - load envelope by reference key
  - decrypt via KMS with the same AAD used at encryption
  - call external model/rerank API
  - delete envelope immediately after use
  - if envelope is missing/expired, retry job later

Recommended Redis envelope fields:

- `kek_id`
- `dek_id`
- `ciphertext`
- `aad`
- `expires_at`

## 13. Non-Goals (v1)

- File descriptors
- POSIX permissions and ownership
- Symlinks
- Advisory/mandatory file locks beyond project-level serialization
- Memory mapping
- Native binary storage protocol
- File version history
- Strict read-after-write consistency for search indexing

## 14. FileIO Console (v1)

Frontend scope in v1:

- Add one `FileIO` entry to the MCP tools list.
- Do not create one dedicated page per FileIO MCP tool.
- The FileIO page must provide:
  - project selector/switcher
  - directory tree browsing within the selected project
  - file content view on click
  - action controls for core FileIO operations
