# Document Summaries in Chunk Retrieval

Verified: 2026-07-10 (UTC)

## Scope

This reference records current primary-source guidance relevant to returning a
document-level summary with each chunk selected by `file_search`. It informs the
design in
[`docs/proposals/file_search_file_summaries.md`](../proposals/file_search_file_summaries.md).

## Findings

### Keep document summaries separate from ranking until evaluated

Anthropic's contextual-retrieval evaluation found that short, chunk-specific context
improved retrieval, while prepending a generic document summary to every chunk had
limited incremental value. A file summary is still useful to the consumer because it
explains the surrounding document, but adding it to embeddings, lexical tokens, or
rerank input is a separate ranking change that needs its own evaluation.

Source: [Anthropic, "Introducing Contextual Retrieval" (2024-09-19)](https://www.anthropic.com/engineering/contextual-retrieval).

### Treat summaries as versioned document metadata

Microsoft's RAG guidance treats a concise summary as standard document or chunk
metadata and recommends source metadata, version tracking, trigger-based incremental
updates, golden datasets, and end-to-end traceability. It also warns that excessive
returned context can obscure the evidence that matters. These points support storing
one bounded summary per file generation and returning it as metadata without replacing
the matched chunk.

Sources:

- [Microsoft, "RAG chunk enrichment phase" (updated 2026-06-18)](https://learn.microsoft.com/en-us/azure/architecture/ai-ml/guide/rag/rag-enrichment-phase).
- [Microsoft, "Build advanced retrieval-augmented generation systems" (updated 2026-01-30)](https://learn.microsoft.com/en-us/azure/developer/ai/advanced-retrieval-augmented-generation).

### Publish summaries through the ingestion lifecycle

OpenAI's retrieval API colocates file identity, score, attributes, and chunk content in
search results. Its file ingestion model is asynchronous and exposes explicit progress
and failure states. The architectural implication for this repository is an inference:
generate a file summary during indexing, bind it to the same source generation as the
chunks, and keep model work off the search hot path. OpenAI's documentation does not
itself prescribe document-summary generation.

Sources:

- [OpenAI, "Retrieval" (accessed 2026-07-10)](https://developers.openai.com/api/docs/guides/retrieval).
- [OpenAI, vector-store file API object (accessed 2026-07-10)](https://developers.openai.com/api/reference/resources/vector_stores/subresources/files).

### Enforce semantic limits in application code

Structured output can enforce a response shape, but it cannot by itself prove that a
summary satisfies a semantic word limit. The service must normalize and count the
decoded text, reject or truncate invalid output deterministically, and apply a bounded
fallback when required.

Source: [OpenAI, "Structured model outputs" (accessed 2026-07-10)](https://developers.openai.com/api/docs/guides/structured-outputs).

### Observe indexing and evaluate retrieval separately from answer quality

Indexer monitoring should expose run state, duration, item counts, warnings, and
document-level failures. Evaluation should separately measure retrieval ranking and
the downstream groundedness, relevance, and completeness gained from the additional
summary context.

Sources:

- [Microsoft, "Monitor indexer status and results" (updated 2026-03-17)](https://learn.microsoft.com/en-us/azure/search/search-monitor-indexers).
- [Microsoft, "RAG evaluators" (updated 2026-06-02)](https://learn.microsoft.com/en-us/azure/foundry/concepts/evaluation-evaluators/rag-evaluators).

## Durable Design Implications

- Generate and persist a summary during ingestion, not per search request.
- Store one summary per file content generation and publish it atomically with the
  corresponding searchable generation.
- Return the original matched chunk and summary together; do not replace chunk content.
- Do not add the generic summary to embeddings, lexical tokens, rerank input, or scores
  without a separate evaluated proposal.
- Keep summaries concise, validate their limit after model decoding, and bound their
  serialized size.
- Treat source files as untrusted data, not instructions, during summary generation.
- Track readiness, latency, retries, failures, stale-generation suppression, and
  backfill progress without logging file content or summaries.
