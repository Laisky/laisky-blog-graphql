# MCP `extract_key_info` Tool Technical Manual

## Scenario Overview

- Provide an MCP tool that retrieves task-specific context for retrieval-augmented generation (RAG) workflows. Task identifiers are normalised by a shared sanitizer used across MCP tools (for example, `get_user_request`) to keep isolation behaviour consistent.
- Accept a user query, raw materials, and a `topK` limit, returning the top matching context snippets.
- Reuse existing MCP server patterns (billing, logging, call auditing) while enforcing tenant isolation with `user_id` and `task_id`.

## Tool Contract

- **Name:** `extract_key_info`
- **Signature:**
  ```go
  func extract_key_info(query string, materials string, topK int) (contexts []string)
  ```
- **Parameters:**
  - `query` (string, required): Natural-language question.
  - `materials` (string, required): Unstructured source text containing candidate answers.
  - `topK` (int, optional, default `5`, max `20`): Number of snippets to return.
- **Return Value:** Ordered slice of context strings suitable for downstream summarisation or answer generation.
- **Error Surface:** Structured MCP errors surfaced for validation failures, billing denials, retrieval issues, or upstream transport problems.

## High-Level Architecture

- **MCP Server (`internal/mcp/server.go`):**
  - Register the tool when embeddings client, PostgreSQL connectivity, and BM25 dependencies are configured.
  - Advertise capability via existing MCP metadata and reuse `recordToolInvocation` with `oneapi.PriceExtractKeyInfo`.
- **Tool Implementation (`internal/mcp/tools/extract_key_info.go`):**
  - Follow existing tool pattern (dependency injection, logger usage, billing checker, API key provider).
  - Encapsulate preprocessing, persistence, hybrid retrieval, and response shaping.
- **Supporting Services:**
  - Embeddings service that calls the OpenAI-compatible endpoint defined in configuration.
  - PostgreSQL 17 with pgvector 0.8.1, VCHORDBM25 0.2.2, and pg_tokenizer 0.1.1 for hybrid search.
  - Optional background workers to maintain indexes when ingesting large materials payloads.

## Configuration and Dependencies

- **Settings:**
  - `settings.openai.base_url`: Override for the embeddings API base.
  - `settings.openai.embedding_model`: Embeddings model identifier (for example `text-embedding-3-small`).
  - `settings.mcp.extract_key_info.enabled`: Feature flag for tool registration.
  - `settings.mcp.extract_key_info.top_k_default`: Default `topK` when omitted by the caller.
  - `settings.db.mcp.*`: PostgreSQL connection parameters shared with other MCP features.
- **Secrets:**
  - Bearer token supplied via `Authorization: Bearer <identity>@<token>`; the raw token doubles as the OpenAI API key.
  - No plaintext secret storage; rely on request-scoped context.
- **Billing:**
  - Introduce `oneapi.PriceExtractKeyInfo` for cost accounting.
  - Invoke `billingChecker` before any embeddings or database work.

## Database Design

### Tables

```sql
CREATE TABLE mcp_rag_tasks (
    id BIGSERIAL PRIMARY KEY,
    user_id TEXT NOT NULL,
    task_id TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (user_id, task_id)
);

CREATE TABLE mcp_rag_documents (
    id BIGSERIAL PRIMARY KEY,
    task_ref BIGINT NOT NULL REFERENCES mcp_rag_tasks (id) ON DELETE CASCADE,
    source_hash TEXT NOT NULL,
    original_text TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE mcp_rag_chunks (
    id BIGSERIAL PRIMARY KEY,
    document_ref BIGINT NOT NULL REFERENCES mcp_rag_documents (id) ON DELETE CASCADE,
    chunk_index INT NOT NULL,
    text TEXT NOT NULL,
    cleaned_text TEXT NOT NULL,
    metadata JSONB NOT NULL DEFAULT '{}'::JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (document_ref, chunk_index)
);

CREATE TABLE mcp_rag_embeddings (
    chunk_ref BIGINT PRIMARY KEY REFERENCES mcp_rag_chunks (id) ON DELETE CASCADE,
    embedding VECTOR(1536) NOT NULL,
    model TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE mcp_rag_bm25 (
    chunk_ref BIGINT PRIMARY KEY REFERENCES mcp_rag_chunks (id) ON DELETE CASCADE,
    tokens bm25vector NOT NULL,
    tokenizer TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

### Indexes and Isolation

```sql
CREATE INDEX idx_mcp_rag_tasks_user_task
    ON mcp_rag_tasks (user_id, task_id);

CREATE INDEX idx_mcp_rag_documents_task
    ON mcp_rag_documents (task_ref);

CREATE INDEX idx_mcp_rag_chunks_task
    ON mcp_rag_chunks (document_ref);

CREATE INDEX idx_mcp_rag_embeddings_vector
    ON mcp_rag_embeddings USING hnsw (embedding vector_cosine_ops)
    WITH (m = 32, ef_construction = 128);

CREATE INDEX idx_mcp_rag_bm25_tokens
    ON mcp_rag_bm25 USING bm25 (tokens bm25_ops);

CREATE INDEX idx_mcp_rag_chunks_metadata
    ON mcp_rag_chunks USING gin (metadata);
```

- Persist `user_id` and `task_id` within chunk metadata for application-level filters and audit queries.
- Consider partitioning `mcp_rag_chunks` by `user_id` for high-volume tenants.
- Maintain triggers to update `updated_at` and cascade deletions cleanly.

## Text Preprocessing Workflow

- Segment `materials` into paragraphs using blank lines; fall back to sentence-level splits when a paragraph exceeds `settings.mcp.extract_key_info.max_chunk_chars` (recommended 1500).
- Normalise whitespace, strip control characters, and store both original and cleaned text versions.
- Deduplicate chunks by hashing `cleaned_text` plus `user_id` plus `task_id`; skip existing entries.
- Persist documents, chunks, embeddings, and BM25 tokens inside a single transaction to ensure atomic ingestion.
- Queue asynchronous ingestion for very large payloads, returning a pending status to the caller when necessary.

## Embedding and BM25 Indexing

- Build embeddings requests using the caller's API key and configured model; batch up to 32 chunks per request for efficiency.
- Implement retry with exponential backoff for rate-limited embedding calls and surface meaningful errors after exhaustion.
- Generate BM25 tokens using `pg_tokenizer`; apply `INSERT ... ON CONFLICT` to refresh existing rows when text changes.
- Store embedding model identifiers and tokenizer versions to support reproducibility and debugging.

## Tool Execution Flow

1.  **Validate Input:** Ensure `topK` is within (0, limit], `query` and `materials` are non-empty, and `materials` size respects configured maximum.
2.  **Authorise and Bill:** Extract API key, perform external billing via `billingChecker`, and abort on denial.
3.  **Resolve Context:** Parse identity header into `user_id` and `task_id`, creating the `mcp_rag_tasks` row when missing.
4.  **Ingest Materials:** When unseen for the given task, run preprocessing and persistence; otherwise skip to retrieval.
5.  **Embed Query:** Produce `query_vector` using the same embeddings model as stored chunks.
6.  **Semantic Search:** Select nearest chunks via pgvector HNSW, filtering by `task_ref` and `user_id`.
7.  **Lexical Search:** Execute BM25 ranking for the same filters.
8.  **Hybrid Scoring:** Normalise semantic and lexical scores, combine with weight (`0.65` semantic, `0.35` lexical), and break ties by recency.
9.  **Assemble Response:** Load chunk text and metadata for the top `topK` rows, preserving rank order.
10. **Audit Call:** Record parameters, scores, duration, and billing outcome through `recordToolInvocation`.

## Error Handling and Logging

- Wrap returned errors with `errors.Wrap` or `errors.Wrapf` to preserve stack traces.
- Obtain request-scoped logger via `gmw.GetLogger(ctx)` and log structured fields (`user_id`, `task_id`, costs, timings`).
- Distinguish retryable errors (transient network, rate limits) from fatal validation errors, mapping to appropriate MCP tool errors.
- Avoid double logging and returning the same error path; follow repository logging guidelines.

## Testing Strategy

- Unit tests covering validation logic, hybrid score fusion, and SQL query builders with testify `require` assertions.
- Integration tests using containerised PostgreSQL with pgvector and VCHORDBM25 to verify ingestion and retrieval flows.
- HTTP-level tests mocking the embeddings endpoint with `httptest.Server`, including rate-limit and error scenarios.
- Tenancy regression tests ensuring users cannot access contexts that belong to other `user_id` or `task_id` combinations.

## Operational Considerations

- Deliver schema changes via `golang-migrate` compatible files and gate roll-outs behind the feature flag.
- Export metrics such as `tool_calls_total`, `embedding_latency_seconds`, and `bm25_latency_seconds` labelled by tenant identifiers.
- Implement retention policies to purge stale tasks and materials (for example, tasks inactive for 90 days).
- Tune HNSW parameters (`m`, `ef_search`) based on corpus size and latency targets; monitor index rebuild times.
- Enforce database access control with per-service roles and TLS connections to PostgreSQL.
