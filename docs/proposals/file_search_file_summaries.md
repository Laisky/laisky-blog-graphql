---
title: File-level summaries in file_search results
status: proposed
owner: mcp team
created: 2026-07-10
updated: 2026-07-10
affected_plugins:
  - rag
  - pageindex
supersedes_on_acceptance:
  - the no-output-change interpretation of docs/requirements/mcp_memory_plugins.md section 2
  - the byte-identical file_search response claims in docs/proposals/mcp_memory_plugin_manager.md A1 and A2
---

# File-level Summaries in `file_search` Results — Change Manual

## 1. Background

### 1.1 Current behavior

`file_search` returns matched chunks with a source path, byte offsets, the original
chunk content, and a relevance score. The shared result type has no document-level
context:

```go
type ChunkEntry struct {
    Project            string
    FilePath           string
    FileSeekStartBytes int64
    FileSeekEndBytes   int64
    IsFullFile         bool
    ChunkContent       string
    Score              float64
}
```

The current RAG pipeline already performs asynchronous indexing after `file_write`:

1. Persist the complete post-write file content and enqueue an `UPSERT` outbox job.
2. Re-read the active file in the index worker.
3. Split it into chunks.
4. Optionally create chunk-specific retrieval context.
5. Build lexical and semantic index rows.
6. Return the original chunk text from `file_search`.

The existing `Index.Summary*` settings and `OpenAIContextualizer` are misleadingly
named for this requirement: they generate short context for each chunk's retrieval
input. They neither produce nor persist one overview for the file.

The PageIndex plugin has a nearby concept, `Tree.DocDescription`, but only its PDF
pipeline currently attempts to populate it. Markdown trees can have no document
description, the output is not capped at 300 words, and search does not copy it into
`ChunkEntry`.

Relevant implementation anchors:

- [shared result type](../../internal/mcp/files/types.go#L37)
- [RAG write and outbox path](../../internal/mcp/files/service_write_delete.go#L74)
- [RAG index worker](../../internal/mcp/files/index_worker.go#L260)
- [chunk contextualizer](../../internal/mcp/files/contextualizer_openai.go#L40)
- [RAG search result mapping](../../internal/mcp/files/service_search.go#L181)
- [PageIndex tree](../../internal/mcp/memory/plugins/pageindex/tree.go#L3)
- [PageIndex search mapping](../../internal/mcp/memory/plugins/pageindex/search_loop.go#L40)

### 1.2 Problem

A matching chunk often lacks enough context to explain what the source file is for,
which topics it covers, or how the match relates to the whole document. Agents must
issue an additional `file_read` or infer the file's purpose from an isolated passage.
That costs latency and tokens and can cause a locally relevant chunk to be interpreted
against the wrong document-level context.

Each saved, user-visible file therefore needs a concise file overview, and every
`file_search` hit must return both:

- the exact matched chunk, preserving byte offsets and score; and
- the overview for the file generation that produced that chunk.

### 1.3 Industry findings

Current primary-source guidance is recorded in
[`docs/ref/file_search_document_summaries.md`](../ref/file_search_document_summaries.md).
The design consequences are:

- Treat the summary as versioned document metadata.
- Generate it during ingestion, not on the search hot path.
- Keep chunk evidence and the file overview as separate fields.
- Do not alter ranking with a generic summary without a separate evaluation.
- Expose readiness, retries, failures, and backfill progress.
- Enforce the length limit in application code after model decoding.

### 1.4 Source-of-truth reconciliation

This proposal is an intentional additive change to the shared `file_search` output
contract. Once accepted, it supersedes only the earlier claims that `file_search`
responses remain byte-identical or that the optional `plugin` input is the only public
schema change. It does not change any existing input field or remove any output field.

The implementation change must amend the following binding documents in the same
change:

- [`docs/requirements/mcp_files.md`](../requirements/mcp_files.md)
- [`docs/requirements/mcp_memory_plugins.md`](../requirements/mcp_memory_plugins.md)
- [`docs/arch/mcp_files.md`](../arch/mcp_files.md)
- [`docs/manual/mcp_files.md`](../manual/mcp_files.md)
- [`docs/manual/mcp_memory_plugins.md`](../manual/mcp_memory_plugins.md)
- [`docs/proposals/mcp_memory_plugin_manager.md`](mcp_memory_plugin_manager.md), by
  annotating the superseded compatibility language rather than silently contradicting it

## 2. Decision, Goals, and Non-goals

### 2.1 Decision

Add a required-on-success `file_summary` string to every returned `ChunkEntry`.
Generate and store one summary per user-visible file content generation. RAG publishes
the summary through its asynchronous index worker; PageIndex reuses or creates its
document description and maps it to the same field.

Summary text is response metadata only in this change. It must not be included in
embedding input, lexical tokens, rerank documents, candidate fusion, or score
calculation.

### 2.2 Goals

- G1. Every `file_search` hit contains a non-empty `file_summary` associated with the
  same source generation as `chunk_content`.
- G2. Summaries are concise English plain text, normally 120–180 words and never more
  than 300 Unicode word segments or 2,048 UTF-8 bytes.
- G3. `file_write` durability and latency do not depend on an external summarization
  request.
- G4. APPEND, OVERWRITE, TRUNCATE, restore, and other content-changing operations
  regenerate the summary from the complete resulting file, not only the request delta.
- G5. Content-preserving rename and move reuse the existing summary.
- G6. RAG, PageIndex, and `plugin="auto"` expose the same public summary semantics.
- G7. Existing ranking, scores, limits, offsets, `is_full_file`, wildcard behavior,
  deletion filtering, and tenant isolation remain unchanged.
- G8. Existing active files receive a safe backfill without platform-key fallback or
  cross-plugin data migration.
- G9. Failures, retries, degraded summaries, freshness, and backfill are observable
  without logging source content or summary text.

### 2.3 Non-goals

- No query-time summary generation.
- No change to `file_search` inputs.
- No summary-based ranking, embedding, BM25 indexing, reranking, or query expansion.
- No summary field on `file_stat`, `file_read`, or `file_list` in this change.
- No summaries for historical `mcp_file_versions`; only the active searchable file
  generation is in scope.
- No relaxation of PageIndex's pure-Go, in-process runtime boundary.
- No automatic migration of a file between RAG and PageIndex.
- No public exposure of model names, prompt versions, content hashes, internal status,
  or raw generation errors.

## 3. Public API Contract

### 3.1 Additive type change

```go
type ChunkEntry struct {
    Project            string  `json:"project,omitempty"`
    FilePath           string  `json:"file_path"`
    FileSeekStartBytes int64   `json:"file_seek_start_bytes"`
    FileSeekEndBytes   int64   `json:"file_seek_end_bytes"`
    IsFullFile         bool    `json:"is_full_file"`
    ChunkContent       string  `json:"chunk_content"`
    FileSummary        string  `json:"file_summary,omitempty"`
    Score              float64 `json:"score"`
}
```

Successful response example:

```json
{
  "chunks": [
    {
      "file_path": "/docs/incident-review.md",
      "file_seek_start_bytes": 1820,
      "file_seek_end_bytes": 2317,
      "is_full_file": false,
      "chunk_content": "The retry queue reached saturation after...",
      "file_summary": "This incident review explains the retry-queue saturation event, its customer impact, contributing deployment and capacity factors, the mitigation timeline, and the follow-up actions assigned to the platform team.",
      "score": 0.93
    }
  ]
}
```

For `project="*"`, the existing `project` field remains present on every chunk. The
summary association is the tuple `(project, file_path, indexed content generation)`, so
identically named paths in different projects cannot collide.

### 3.2 Per-chunk repetition is intentional

`file_summary` is repeated on each matching chunk, including multiple hits from the
same file. This keeps every result self-contained, preserves the existing
`{"chunks": [...]}` envelope, and avoids a second lookup structure that older clients
do not understand.

The cost is bounded by both summary limits. With the current maximum `limit=20`, the
summary-specific contribution is at most 40,960 UTF-8 bytes before JSON escaping.
The implementation must also serialize the complete `mcp.CallToolResult` at
`limit=20`, including JSON escaping and any MCP text/structured-content wrapper, and
prove that the default configuration stays within both 128 KiB and the deployed MCP
output-token budget. `file_summary` cannot be copied into two content channels. If
`mcp.NewToolResultJSON` duplicates the payload, construct one canonical JSON text block
instead. Future response normalization or references require a separate versioned
proposal.

### 3.3 Compatibility rules

- Existing fields retain their names, types, meaning, and omission rules.
- `file_summary` is additive and non-empty for every returned chunk after the rollout
  enforcement gate is enabled.
- Before enforcement, `omitempty` or a gated response DTO omits the field entirely for
  rows without a ready/degraded summary. Rollback uses the same gate to restore the
  original wire shape; it must not emit `"file_summary":""`.
- Clients that ignore unknown JSON fields continue to work.
- Search ranking and result count do not change because of summary text.
- Search errors retain the existing machine-stable error format.
- The MCP tool description must state that each hit includes matched content and a
  concise file-level summary.

### 3.4 Summary format and limits

The persisted and returned summary must satisfy all rules below:

- English plain text, regardless of the source language.
- One paragraph with surrounding whitespace removed.
- Target length: 120–180 words for substantive documents.
- Hard length: at most 300 Unicode word segments.
- Hard serialized text size: at most 2,048 UTF-8 bytes.
- No Markdown fence, HTML, tool call, URL invented by the model, or execution
  instruction.
- Grounded only in the saved file. Unknown facts are omitted, not guessed.
- Covers purpose, scope, major topics or entities, material dates or versions, and
  important limitations when those facts exist.

The shared normalizer is authoritative. Prompt instructions and model output-token
limits are defenses in depth, not proof of compliance. Word counting follows Unicode
text segmentation. Operators may configure a lower limit but may not raise either hard
maximum.

Empty or opaque content uses a deterministic English fallback, for example `Empty
file.` or `This file contains encoded or non-natural-language text; no reliable
semantic overview is available.` A fallback is valid but is marked internally as
degraded.

## 4. Summary and Index Lifecycle

### 4.1 Required generation flow

```text
file_write / restore
        |
        | transaction: save complete content + content hash + outbox job
        v
  durable file row -------------------------------> file_write returns
        |
        v
 index/summary worker
        |
        +--> re-read complete active file
        +--> reject stale job by content hash
        +--> generate and validate one file summary
        +--> build plugin-specific searchable representation
        +--> re-check active content hash
        +--> atomically publish summary + corresponding index generation
        v
 file_search returns matched chunk + file_summary
```

No external model request may run while the project mutation transaction or advisory
lock is held.

### 4.2 Content-generation binding

The pipeline must use an immutable SHA-256 content hash as the generation identity:

- `mcp_files.content_hash` identifies the current saved content.
- `mcp_file_chunks.file_content_hash` identifies the content used for a RAG chunk.
- `mcp_files.summary_content_hash` identifies the content used for the persisted
  summary.
- `PageIndex.Tree.SourceContentHash` identifies the content used for both its cached
  pages and `DocDescription`.
- The index job carries the expected content hash in addition to `file_updated_at`.
- Search may pair a chunk and summary only when their content hashes match.

The worker rechecks the active file hash immediately before commit. If a newer write
arrived while an LLM call was in flight, the stale worker must discard its output and
must not overwrite the newer summary state.

For an updated file, the previous complete chunk-and-summary generation may remain
searchable until the new generation is atomically published. This is the existing
eventual-consistency model. A search result must never mix generations.

### 4.3 Mutation semantics

| Operation            | Required summary behavior                                                                                                                  |
| -------------------- | ------------------------------------------------------------------------------------------------------------------------------------------ |
| Create               | Enqueue summary generation for the complete new file. The file is readable immediately and searchable when a complete generation is ready. |
| APPEND               | Summarize the complete file after append, not only appended bytes.                                                                         |
| OVERWRITE            | Summarize the complete post-overwrite file, including the unchanged tail.                                                                  |
| TRUNCATE             | Summarize the complete replacement content.                                                                                                |
| Restore version      | Treat restored bytes as a new active content generation.                                                                                   |
| Rename file          | Reuse the summary and content hash; move summary metadata with the file row. Do not make a model call solely because the path changed.     |
| Move directory       | Apply the file-rename rule independently to every moved file.                                                                              |
| Delete               | Preserve soft-deleted metadata only for retention; active-file filtering must make chunks and summaries immediately unreachable.           |
| Rewrite after delete | Treat the active row as a new content generation and do not expose a retained deleted summary unless hashes match.                         |

### 4.4 RAG plugin

The RAG `UPSERT` worker generates one file summary before it publishes new chunk rows.
It must use the complete content already reread by `processUpsertJob` and the caller's
credential envelope. Platform-key fallback remains forbidden.

The summary generator is separate from the existing chunk `Contextualizer`:

```go
type FileSummarizer interface {
    GenerateFileSummary(
        ctx context.Context,
        apiKey string,
        wholeDocument string,
    ) (string, error)
}
```

The source path is deliberately excluded from generation input. Summary text describes
the saved content only, so a content-preserving rename can reuse it without leaving a
stale path in the overview.

For inputs that exceed the model context budget, the implementation uses bounded
hierarchical summarization: summarize deterministic source ranges, reduce those partial
summaries, then apply the shared final validator. The number of calls and input tokens
must be configurable and bounded. Exhausting the budget uses the degraded fallback; it
must not allocate proportionally to untrusted declared sizes.

The current `index.summary.*` settings continue to configure chunk contextualization
for backward compatibility. New `index.file_summary.*` keys configure the file-level
summary so the two concepts are not conflated.

When `WriteOpts.SkipRAGIndex=true`, the RAG worker remains fully skipped. The owning
plugin must publish summary metadata through the generation-safe service method in
section 4.5. Rows with non-empty `system_owner` are plugin-internal and do not receive
user-facing summaries or summary jobs.

### 4.5 PageIndex plugin

PageIndex must populate the same `FileSummary` field without introducing another
runtime or service:

- PDF: reuse `Tree.DocDescription` when present.
- Markdown: generate a document description from the completed tree; do not rely only
  on leaf-node summaries.
- If document-description generation is disabled, fails, or returns invalid output,
  build the shared deterministic fallback from the parsed document/tree.
- Apply the same English, word, and byte limits before persisting the tree.
- Persist `SourceContentHash` on the tree and reject publication when it no longer
  matches the active file.
- Copy the validated description into every `ChunkEntry` produced by the search loop.
- Continue to use the pure-Go, in-process PageIndex implementation. No subprocess,
  sidecar, cgo, or IPC is permitted.

For PageIndex search hits, `Tree.DocDescription` is the authoritative public summary.
The copy on the user `mcp_files` row is catalog metadata and must equal the tree value,
but the search loop must not choose between two authorities.

Add one generation-safe PageIndex publisher backed by a single database transaction.
After PageIndex finishes model work, the publisher must:

1. acquire the project mutation lock;
2. re-read the active user file and compare its `content_hash` with
   `Tree.SourceContentHash`;
3. reject the stale publication when the hashes differ;
4. write the tree JSON, its `IndexEntry` (also carrying `SourceContentHash`), the
   path-to-document mapping, and the identical user-row summary metadata; and
5. commit all rows together or roll them all back.

An older, slower PageIndex write therefore cannot overwrite the tree or summary from a
newer write. Search exposes only a tree/index mapping committed by this publisher.

PageIndex currently indexes only `.pdf` and `.md`. Other user-visible files remain
stored but are not PageIndex search candidates. The PageIndex plugin publishes a local
deterministic summary for these files through the same conditional user-row summary
method; it does not enqueue a RAG job or borrow a RAG caller credential. This satisfies
the per-file storage requirement without pretending that PageIndex can search an
unsupported format.

PageIndex must also correct its content-update input: APPEND, OVERWRITE, and TRUNCATE
index and summarize the complete post-write file read from storage, not merely the
`content` argument supplied to `file_write`.

Credential policy remains plugin-specific and unchanged: RAG summary calls use the
caller's encrypted short-lived credential handoff, while PageIndex reuses its accepted
configured Responses API client and key. Neither plugin may silently substitute a
different key when its required credential is unavailable.

### 4.6 Failure and degraded behavior

| Condition                                    | Behavior                                                                                                                                                               |
| -------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Model timeout, 429, or 5xx                   | Publish a bounded deterministic fallback with the same generation, retain a retryable refresh state, and retry with bounded backoff.                                   |
| Missing or expired RAG caller credential     | Never use a platform key. Publish the deterministic fallback, record a safe degraded reason, and refresh only when an authenticated operation supplies a new envelope. |
| Empty model output                           | Reject it and use the fallback.                                                                                                                                        |
| Oversized output                             | Reject it, normalize once if deterministic truncation preserves a complete sentence, otherwise use the fallback. Never return more than the hard limits.               |
| Malformed structured output                  | Reject it and use the fallback.                                                                                                                                        |
| New write during generation                  | Discard the stale generation and let the latest job win.                                                                                                               |
| Worker crash after generation, before commit | Retry idempotently; no partial generation is visible.                                                                                                                  |
| Summary persistence failure                  | Roll back the corresponding index publication.                                                                                                                         |
| Terminal refresh failure                     | Keep the degraded summary searchable, mark the refresh failed internally, and alert operators.                                                                         |

Fallback publication and model refresh use distinct job state. An `UPSERT` job may
atomically commit fallback summary + chunks and complete successfully. In that same
transaction it enqueues a deduplicated `SUMMARY_REFRESH` job keyed by
`(system_owner, apikey_hash, project, path, content_hash, summary_generation_key)`.

`SUMMARY_REFRESH` stores `retry_count`, `available_at`, safe `last_error_code`, and
status `pending | processing | waiting_auth | done | failed`. It updates only summary
metadata after rechecking the active content hash; it never rebuilds or reranks chunks.
Transient provider failures requeue with bounded backoff. An expired credential moves
the job to `waiting_auth`; the next authenticated mutation or explicit reindex for the
same generation may attach a new encrypted envelope and reactivate it. Retry exhaustion
marks only the refresh job failed—the already published deterministic summary remains
`degraded` and searchable. Credential payloads are deleted immediately after use and
never copied into the job row.

This policy preserves availability and guarantees that every returned hit has a
summary. `file_write` success continues to mean the file is durable, not that the
asynchronous model call succeeded.

### 4.7 Raw-file search fallback

The current RAG implementation can scan raw active files before index rows are ready.
That path may return a chunk with no summary and therefore conflicts with the new
contract.

After rollout enforcement:

- raw-file fallback may return a result only when a persisted summary exists for the
  exact current `content_hash`; and
- otherwise it returns no hit until the worker publishes a complete generation.

It must not generate an unpersisted summary on the query path or combine a new raw
snippet with the previous generation's summary.

## 5. Persistence, Migration, and Rollout

### 5.1 Schema changes

Add the following logical fields to `mcp_files`:

| Field                    | Purpose                                                   |
| ------------------------ | --------------------------------------------------------- |
| `content_hash`           | SHA-256 of the current stored bytes.                      |
| `file_summary`           | Validated file-level overview; nullable during migration. |
| `summary_content_hash`   | Source generation represented by `file_summary`.          |
| `summary_word_count`     | Validator result used for metrics and audits.             |
| `summary_source`         | `model`, `pageindex`, or `deterministic_fallback`.        |
| `summary_model`          | Internal model identifier; empty for fallback.            |
| `summary_prompt_version` | Internal prompt/algorithm version.                        |
| `summary_generation_key` | Hash of content, model, prompt, and effective limits.     |
| `summary_status`         | `pending`, `ready`, `degraded`, or `failed`.              |
| `summary_updated_at`     | UTC publication time.                                     |
| `summary_error_code`     | Safe machine code only; never raw provider output.        |

Add `file_content_hash` to `mcp_file_chunks` and the expected `content_hash` to
`mcp_file_index_jobs`. Extend the job operation/status model for the
`SUMMARY_REFRESH` lifecycle in section 4.6. Existing timestamp-based stale-job checks
remain as defense in depth, but the hash is authoritative.

`summary_generation_key` is SHA-256 over the content hash, model, prompt version, and
effective limits. A model, prompt, or limit change therefore schedules a refresh even
when file bytes are unchanged.

Migrations must be additive and idempotent for PostgreSQL and SQLite. Add nullable
columns first, backfill them, then enforce any new `NOT NULL` constraint in a later
deployment. Every SQL statement touching an `mcp_file*` table must include an explicit
`system_owner = ?` predicate or the required justified annotation.

### 5.2 Backfill

Existing files do not have durable caller credential envelopes, so the migration must
not attempt an offline platform-key LLM backfill.

Backfill in bounded, restart-safe batches:

1. Select active user rows with `system_owner=''` and missing `content_hash` or summary
   state.
2. Compute and persist `content_hash` from stored bytes.
3. Create a deterministic, bounded fallback summary locally and mark it `degraded`.
4. Replay the exact versioned chunker against current bytes. Associate legacy rows with
   the content hash only when the full ordered set has identical chunk count, indexes,
   ranges, text, and per-chunk hashes. A valid substring or partial coverage is not
   sufficient. Otherwise require a normal authenticated reindex before enforcement.
5. On the next authenticated write or explicit reindex, use the caller credential
   envelope to upgrade the fallback to a model summary.

PageIndex backfill reuses a valid, capped `Tree.DocDescription` only when the tree has a
verifiable `SourceContentHash` matching the active file. Legacy trees without that hash
are reindexed through the configured PageIndex client; enforcement does not start
while an unverifiable tree remains. No file is migrated between plugins.

### 5.3 Phased rollout

| Phase        | Deployment behavior                                                                                         | Exit gate                                                                            |
| ------------ | ----------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------ |
| 0. Baseline  | Freeze ranking, latency, response-size, and quality fixtures.                                               | Baseline artifacts reviewed.                                                         |
| 1. Schema    | Add nullable columns and dual-compatible readers. Do not emit `file_summary` yet.                           | Migrations succeed twice on PostgreSQL and SQLite.                                   |
| 2. Producers | Deploy RAG and PageIndex summary producers plus metrics. Persist but do not require summaries in responses. | New writes reach ready or degraded summary state within the plugin freshness window. |
| 3. Backfill  | Backfill hashes and deterministic summaries in bounded batches.                                             | All active searchable generations have a valid summary; zero cross-owner mismatches. |
| 4. Response  | Emit `file_summary` for every search hit and enforce generation equality.                                   | Contract, conformance, and e2e matrix passes.                                        |
| 5. Cleanup   | Remove temporary dual-read paths only after one full rollback window.                                       | No legacy rows or missing-summary hits for the agreed observation period.            |

The response field must not be enabled before the backfill gate, otherwise clients
would observe a mix of missing and non-empty summaries.

### 5.4 Rollback

- Disable response emission first; older clients continue to receive the original
  shape.
- Stop summary refresh work while leaving existing index workers operational.
- Leave additive columns in place during the rollback window; dropping them is a later
  migration, never an emergency rollback step.
- Do not roll back the content-generation guards independently from summary emission if
  doing so could pair stale summaries and chunks.
- PageIndex tree JSON remains forward-compatible because `doc_description` is already
  optional.

## 6. Change List

### 6.1 Shared contract and tool layer

- Add `FileSummary` to `files.ChunkEntry` and the plugin type aliases.
- Update the `file_search` MCP description and response examples.
- Keep the existing `{"chunks": [...]}` envelope and all input schemas unchanged.
- Extend output redaction so logs and call audits never persist `file_summary`.

### 6.2 RAG persistence and indexing

- Add idempotent migration columns and model fields in `internal/mcp/files`.
- Compute the complete file `content_hash` on all write and restore paths.
- Carry the expected hash in index jobs and credential references.
- Add a `FileSummarizer` and OpenAI-compatible implementation with bounded
  hierarchical input handling.
- Add the shared summary normalizer, Unicode word counter, byte cap, and deterministic
  fallback.
- Extend `processUpsertJob` to generate one summary per file generation.
- Publish chunk rows and summary metadata in one transaction after a final stale-hash
  check.
- Add the distinct `SUMMARY_REFRESH` lifecycle and authenticated reactivation behavior
  from section 4.6.
- Extend every semantic, lexical, in-memory, wildcard, and raw-fallback query branch to
  carry the matching summary.
- Preserve `last_served_at` semantics: update only final chunks, not summaries or
  candidates.

### 6.3 PageIndex

- Enforce the shared summary limits on `Tree.DocDescription`.
- Add and enforce `Tree.SourceContentHash` and `IndexEntry.SourceContentHash`.
- Generate a document description for Markdown trees.
- Add deterministic tree-based fallback behavior.
- Re-read complete post-write content before indexing APPEND, OVERWRITE, or TRUNCATE.
- Publish tree, index mapping, and user-row summary metadata through one conditional
  transaction.
- Publish deterministic user-row summaries for unsupported, non-searchable extensions
  without routing them through RAG.
- Map the document description into every returned `ChunkEntry`.
- Extend PageIndex golden prompts and fixtures without adding a non-Go runtime.

### 6.4 Configuration

Add RAG keys under `settings.mcp.tools.memory.plugins.rag.index.file_summary.*`, with
legacy-path fallback during the existing compatibility window:

- `enabled` (default `true`; disabling is allowed only before response enforcement)
- `model` (defaults to the existing chunk-context model unless set)
- `base_url` (defaults to the existing OpenAI-compatible base URL)
- `timeout_ms`
- `target_words` (default `160`, maximum `300`)
- `max_words` (default and hard maximum `300`; lower values allowed)
- `max_bytes` (default and hard maximum `2048`; lower values allowed)
- `max_input_tokens` (default `16000` per call)
- `max_reduce_calls` (default `8`)
- `max_total_input_tokens` (default `64000` per file generation)
- `max_total_output_tokens` (default `4096` per file generation)
- `prompt_version`

Document clearly that the existing `index.summary.*` keys still configure
chunk-specific contextualization, not `file_summary`.

### 6.5 Documentation and operations

- Amend the six source-of-truth documents listed in section 1.4.
- Update `docs/example/config/settings.yml` with safe summary defaults.
- Add dashboard panels and alerts from section 7.
- Add a backfill/reindex runbook with pause, resume, and rollback instructions.
- Record any frozen evaluation baseline reset in the existing eval artifacts and
  changelog process.

## 7. Observability and Security

### 7.1 Metrics

Expose at least:

- `mcp_files_summary_generation_total{plugin,status,source}`
- `mcp_files_summary_generation_latency_ms{plugin,source}`
- `mcp_files_summary_word_count{plugin,source}`
- `mcp_files_summary_model_calls_total{plugin,model}`
- `mcp_files_summary_tokens_total{plugin,model,direction}`
- `mcp_files_summary_estimated_cost_usd_total{plugin,model}`
- `mcp_files_summary_retry_total{plugin,reason}`
- `mcp_files_summary_refresh_total{plugin,reason,status}`
- `mcp_files_summary_degraded_total{plugin,reason}`
- `mcp_files_summary_stale_discard_total{plugin}`
- `mcp_files_summary_missing_hit_total{plugin}`; this must remain zero after enforcement
- `mcp_files_summary_backfill_remaining`
- `mcp_files_index_visibility_lag_ms{plugin}` including summary readiness

Alerts:

- any missing-summary hit after enforcement;
- summary-ready freshness above the plugin-advertised window;
- sustained degraded or failed summary rate above 1%;
- stale-generation overwrite attempt above zero;
- backfill progress stalled for two worker intervals.

### 7.2 Logging and redaction

Logs may contain safe tenant-scoped identifiers, project, path, content hash prefix,
job ID, status, latency, word count, source, and safe error code. They must not contain:

- source file content or previews used for summarization;
- generated summary text;
- prompts or provider response bodies;
- raw API keys, credential envelopes, tokens, or authorization headers;
- unsanitized model errors that can echo source content.

Errors are processed exactly once and use the repository error and structured-logging
conventions.

### 7.3 Prompt-injection and output safety

- Delimit file content as untrusted data and state that embedded instructions must not
  be followed.
- The summarizer cannot invoke tools, browse, fetch URLs, or mutate files.
- Use low-variance generation and a strict response shape where supported.
- Validate decoded output independently of model compliance.
- Escape summary text at any HTML rendering boundary; JSON/MCP serialization remains
  ordinary structured data.
- Do not reveal system prompts, credentials, internal paths, or facts absent from the
  file.

### 7.4 Tenant and namespace isolation

All summary reads and writes must preserve the existing scope:

```text
(system_owner, apikey_hash, project, path)
```

Cross-project wildcard search may omit only the project predicate. It must retain
`system_owner` and `apikey_hash` filtering, and every hit must carry its source project.
Plugin-internal PageIndex rows remain unreachable through user-facing tools.

## 8. Test Matrix

Tests use `github.com/stretchr/testify/require`. User-observable behavior is the
acceptance contract; unit tests, SQL assertions, and mocks are evidence.

### 8.1 Contract and summary validation

| ID  | Scenario                                                              | Expected result                                                                                        |
| --- | --------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------ |
| C01 | Search one indexed text file                                          | Every hit contains the unchanged chunk fields plus a non-empty `file_summary`.                         |
| C02 | Search returns multiple chunks from one file                          | Each hit repeats the same summary and preserves independent offsets and scores.                        |
| C03 | Search returns identical paths from two projects with `project="*"`   | Each hit has the correct source project and that project's summary.                                    |
| C04 | Model returns 299, 300, and 301 Unicode words                         | 299 and 300 pass; 301 is never persisted or returned without compliant normalization/fallback.         |
| C05 | Model returns over 2,048 bytes but under 300 words                    | Returned summary is at most 2,048 bytes and remains valid UTF-8.                                       |
| C06 | Source is Chinese, Japanese, Arabic, or mixed-language                | Summary is grounded English and both Unicode-word and byte caps hold.                                  |
| C07 | Empty, one-line, code-only, Base64-like, and very long files          | Each stored file reaches ready or degraded summary state; any search hit has a useful bounded summary. |
| C08 | Source contains summary prompt injection and fake system instructions | Output ignores instructions, performs no action, leaks no prompt, and summarizes only file evidence.   |

### 8.2 Mutation and consistency

| ID  | Scenario                                                  | Expected result                                                                                                             |
| --- | --------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------- |
| M01 | Create then search immediately and after freshness window | Immediate read succeeds; search becomes available only as a complete chunk-summary generation within the advertised window. |
| M02 | APPEND                                                    | Summary reflects the complete appended file; returned offsets still slice the stored bytes.                                 |
| M03 | OVERWRITE with unchanged tail                             | Summary covers the complete post-overwrite content, including the tail.                                                     |
| M04 | TRUNCATE                                                  | No old topic remains in the new summary or chunks.                                                                          |
| M05 | Restore a historical version                              | Restored bytes receive a new generation and matching summary.                                                               |
| M06 | Rename a file                                             | The same summary appears under the new path within the rename freshness window, and the old path returns no hit.            |
| M07 | Move a directory containing many files                    | Every moved hit has its original file's summary under the new path; no cross-file swap occurs.                              |
| M08 | Delete a file during search                               | No subsequent result contains its chunk or summary.                                                                         |
| M09 | Old worker finishes after a newer write                   | Old output is discarded; the latest complete generation wins.                                                               |
| M10 | Duplicate job delivery and worker restart                 | Publication is idempotent; no duplicate rows or mixed summary generations appear.                                           |
| M11 | Concurrent writes and searches under `-race`              | Every observed hit is internally generation-consistent; no data race or partial JSON occurs.                                |

### 8.3 Failure and recovery

| ID  | Scenario                                                     | Expected result                                                                                                |
| --- | ------------------------------------------------------------ | -------------------------------------------------------------------------------------------------------------- |
| F01 | Summary endpoint times out or returns 429/5xx                | A deterministic fallback is published, a bounded refresh retry is observable, and write success is unchanged.  |
| F02 | Credential envelope is missing or expired                    | No platform key is used; fallback remains available and logs expose only a safe reason.                        |
| F03 | Model returns empty, malformed, HTML, or oversized output    | Invalid output is never public; validator selects a compliant fallback.                                        |
| F04 | DB fails while publishing summary after chunks were prepared | The transaction rolls back; search sees the previous complete generation or no new-file result.                |
| F05 | Context cancellation during model call                       | Worker stops promptly, job remains retryable, and no partial generation is visible.                            |
| F06 | Retry limit is exhausted                                     | Degraded summary remains searchable, terminal refresh failure is visible to operators, and no hot loop occurs. |
| F07 | Raw fallback runs before first index publication             | It returns no unsummarized hit and never performs a query-time model call.                                     |

### 8.4 Isolation and deletion

| ID  | Scenario                                                  | Expected result                                                                                  |
| --- | --------------------------------------------------------- | ------------------------------------------------------------------------------------------------ |
| S01 | Tenant B searches the same project/path names as tenant A | Zero chunks and zero summaries from tenant A across 100 randomized probes.                       |
| S02 | Wildcard search across caller-owned projects              | Only caller-owned projects appear; each summary matches its chunk's project and path.            |
| S03 | Search with `path_prefix`                                 | Existing prefix semantics and ranking remain unchanged; summaries come only from matching paths. |
| S04 | User search targets PageIndex system paths                | No path, chunk, summary, size, or timing signal reveals `system_owner!=''` rows.                 |
| S05 | Soft-deleted row remains during retention                 | It is never returned by semantic, lexical, rerank-fallback, or raw-fallback branches.            |
| S06 | Logs, call audit, and HTTP hooks capture a search         | Captured output contains no file content, summary text, prompt, API key, or envelope.            |

### 8.5 Plugin and end-to-end parity

| ID  | Scenario                                       | Expected result                                                                                    |
| --- | ---------------------------------------------- | -------------------------------------------------------------------------------------------------- |
| P01 | RAG write → worker → search                    | Summary comes from the full file generation and all legacy fields remain correct.                  |
| P02 | PageIndex PDF write → search                   | `Tree.DocDescription` is capped, persisted, and returned on every selected page chunk.             |
| P03 | PageIndex Markdown write → search              | A document description is generated or deterministically derived and returned on every hit.        |
| P04 | PageIndex APPEND/OVERWRITE/TRUNCATE            | Indexer reads complete post-write content and summary/chunks match it.                             |
| P05 | PageIndex unsupported search extension         | File is stored and summarized, but it is not falsely exposed as a PageIndex search hit.            |
| P06 | Two PageIndex writes finish in reverse order   | Search returns only the newest file's tree, chunks, and summary; the older publisher is discarded. |
| P07 | `plugin="auto"` with either configured default | Response summary semantics match the resolved plugin.                                              |
| P08 | Shared plugin conformance suite                | Every plugin returns a non-empty capped summary for each successful search hit.                    |
| P09 | Pure-Go gate                                   | PageIndex introduces no cgo, subprocess, IPC, sidecar, or external runtime.                        |

### 8.6 Migration, quality, and performance

Freeze the reproducible summary evaluation under `docs/eval/file_summary_v1/` before
implementation acceptance:

- `corpus.jsonl`: at least 200 labeled files, stratified across length, text/code,
  Markdown/PDF, source language, empty/opaque content, and injection cases;
- `rubric.md`: claim-groundedness, key-topic coverage, critical-error, and degraded
  fallback rules;
- `judge_config.yml`: immutable judge model ID, exact prompt hash, temperature, seed
  where supported, and score thresholds;
- `raw_results.jsonl`, `scorecard.md`, and `run_metadata.yml`: per-file scores, generator
  model/prompt/limits, token and call counts, code revision, and dependency versions;
- a stratified 50-file human audit by two reviewers, with disagreements adjudicated
  and critical unsupported claims listed explicitly.

A floating judge alias or an undocumented prompt change invalidates the run. Model,
prompt, or effective-limit changes must alter `summary_generation_key` and exercise the
refresh path.

| ID  | Scenario                                                | Expected result                                                                                                                                                     |
| --- | ------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Q01 | Run migrations twice on populated PostgreSQL and SQLite | Both runs succeed; active files, versions, chunks, and jobs remain intact.                                                                                          |
| Q02 | Interrupt and resume backfill                           | Work resumes without duplicates; remaining count decreases monotonically.                                                                                           |
| Q03 | Legacy files have no credential envelope                | Local backfill produces compliant degraded summaries without platform-key calls.                                                                                    |
| Q04 | Legacy PageIndex tree lacks a source hash               | The document is reindexed; no unverifiable tree is exposed after enforcement.                                                                                       |
| Q05 | Frozen retrieval corpus before and after change         | Public project/path, offsets, chunk content, scores, and order are identical except predeclared score ties.                                                         |
| Q06 | Golden summary corpus across formats and languages      | Mean groundedness is at least 0.90, key-topic coverage at least 0.80, and human review finds zero critical unsupported claims.                                      |
| Q07 | RAG `file_search` load test                             | p95 latency is at most baseline ×1.05 and p99 at most baseline ×1.10.                                                                                               |
| Q08 | RAG indexing throughput                                 | At least the existing 1,500 text-pages/minute/worker gate.                                                                                                          |
| Q09 | Maximum limit and worst-case escapable summaries        | The fully serialized `CallToolResult` at `limit=20` is at most 128 KiB, fits the deployed token budget, carries one payload copy, and uses bounded memory.          |
| Q10 | Freshness test with healthy dependencies                | RAG summary+chunk visibility meets its advertised 5-second window and the older 30-second FileIO ceiling; PageIndex preserves its synchronous zero-window contract. |
| Q11 | Eval comparison with and without returned summaries     | Retrieval metrics do not regress; downstream groundedness, relevance, and completeness are reported separately.                                                     |
| Q12 | Healthy-provider summary state distribution             | At least 99% of searchable generations are model/pageindex `ready`; degraded generations remain at or below 1%.                                                     |
| Q13 | Deterministic fallback subset                           | Every fallback is grounded and bounded, and mean key-topic coverage is at least 0.50 on the labeled fallback subset.                                                |
| Q14 | Maximum summarization budget                            | Calls and input/output tokens never exceed configured per-file totals; cancellation stops further calls and all usage is recorded.                                  |
| Q15 | Model, prompt, or limit configuration changes           | The generation key changes and schedules one deduplicated refresh without rebuilding or reranking chunks.                                                           |

### 8.7 Required verification commands

After implementation:

```bash
make lint
go test -race -cover ./...
go test -bench ./...
```

Run the plugin conformance and frozen evaluation targets for both RAG and PageIndex.
Frontend commands are required only if the FileIO console is changed to render the new
field.

### 8.8 Internal regression evidence

These checks are required engineering evidence, not substitutes for the observable
acceptance outcomes in section 9:

- persisted RAG chunk and summary hashes are equal for every returned candidate;
- the PageIndex publisher rejects an older writer after a newer file commit and
  atomically rolls back tree, mapping, and summary rows on failure;
- rename makes zero summary-provider calls when content is unchanged;
- model calls and tokens remain within configured totals, including cancellation;
- every tracked `mcp_file*` SQL statement passes the `system_owner` predicate gate;
- the PageIndex pure-Go gate rejects cgo, process execution, IPC, or sidecar artifacts;
- `make lint`, race/coverage tests, PostgreSQL integration tests, and benchmarks pass.

## 9. Acceptance Criteria

Acceptance is stated in caller- or operator-observable terms. Section 8.8 records the
internal evidence used to prove those outcomes.

A1. **Every successful hit is self-contained.** When an agent calls `file_search`, every
returned chunk contains a non-empty `file_summary` plus all previous fields with their
previous meanings. A client that ignores the new field continues to parse the response.

A2. **The summary is bounded and useful.** Every returned summary is English plain text,
at most 300 Unicode word segments and 2,048 UTF-8 bytes. On the approved golden corpus,
mean groundedness is at least 0.90, key-topic coverage is at least 0.80, and human review
finds no critical unsupported claim under the frozen section 8.6 protocol.

A3. **Chunks and summaries never cross generations.** When two concurrent file versions
contain distinct version markers and topics, every returned summary describes the same
version and topic as its returned chunk. A stale worker, retry, restart, or concurrent
write never produces a mixed pair.

A4. **All content-changing saves refresh the overview.** Create, APPEND, OVERWRITE,
TRUNCATE, and restore summarize the complete resulting file. Rename and move reuse the
summary when bytes are unchanged. Deleted files expose neither chunk nor summary.

A5. **Writes do not wait for the model.** Under a slow or unavailable summary provider,
`file_write` commits with its established latency profile. Search later returns either a
validated model summary or an explicitly tracked deterministic fallback; it never
returns an empty field. RAG never substitutes a platform key for a missing caller
credential.

A6. **Isolation is absolute.** Across tenant, project, wildcard, path-prefix, deleted-row,
and system-namespace probes, no caller receives or infers another owner's summary,
including through path existence, sizes, errors, or measurable timing differences.

A7. **Plugin semantics are uniform.** When an operator switches the default between
RAG and PageIndex, or a caller uses `auto`, PDF and Markdown hits expose the same public
summary field and limit semantics without changing the tool-call shape.

A8. **Ranking is unchanged.** On the frozen retrieval corpus, public project/path,
offsets, chunk content, scores, and order remain unchanged except documented score
ties. Summary text is not part of any ranking input in this change.

A9. **Freshness and performance stay within existing gates.** Healthy RAG indexing
publishes a complete summary+chunk generation within its advertised 5-second window
and remains inside the 30-second FileIO ceiling. `file_search` p95 is no more than
baseline ×1.05 and p99 no more than baseline ×1.10. PageIndex retains its synchronous
freshness contract. A worst-case `limit=20` response fits the 128 KiB and deployed
output-token budgets as a single serialized payload.

A10. **Rollout is complete before enforcement.** Idempotent migrations, producer
deployment, bounded backfill, and missing-summary audits finish before response
enforcement. At enforcement, callers observe zero missing-summary hits, at least 99%
ready summaries under healthy providers, and no more than 1% degraded summaries.

A11. **Operations are diagnosable without content leakage.** Dashboards distinguish
ready, degraded, retried, failed, stale-discarded, and backfilled summaries. Logs and
audits contain no source content, summary text, prompt, provider body, API key, token,
or credential envelope.

A12. **Documentation and runtime agree.** An operator following the updated manuals and
examples observes the documented response, readiness, fallback, configuration, and
rollback behavior under both plugins. Requirements, architecture, manuals, examples,
and shipped MCP behavior describe the same contract.

## 10. Implementation Order

1. Freeze baseline fixtures and add failing contract/generation-consistency tests.
2. Land additive migrations and dual-read model fields.
3. Implement the shared normalizer, fallback, content-hash binding, and RAG producer.
4. Implement PageIndex description parity and complete-content updates.
5. Add search mapping without enabling public enforcement.
6. Backfill and audit existing active files.
7. Enable `file_summary`, run the full matrix, and update all source-of-truth documents.
8. Observe one rollback window before removing compatibility code.

The feature is accepted only after section 9 is satisfied; green legacy tests alone are
not sufficient evidence.
