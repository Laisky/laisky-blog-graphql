# Document Summaries in Chunk Retrieval

Verified: 2026-07-11 (UTC)

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

### Version summaries by content hash in the ingestion pipeline

LlamaIndex's ingestion pipeline is the reference pattern for incremental re-ingestion:
documents are keyed by ID plus content hash, and transformations re-run only when the
hash changes. Its document-summary index likewise generates summaries at build time,
not query time. This supports binding one persisted summary to one content generation
and regenerating on hash mismatch.

Sources:

- [LlamaIndex, "Ingestion pipeline" (accessed 2026-07-11)](https://docs.llamaindex.ai/en/stable/module_guides/loading/ingestion_pipeline/).
- [LlamaIndex, "A new document summary index" (2023-05)](https://www.llamaindex.ai/blog/a-new-document-summary-index-for-llm-powered-qa-systems-9a32ece2f9ec).

### Keep the MCP dual-channel result shape and advertise an output schema

The MCP specification recommends that a tool returning `structuredContent` also return
the functionally equivalent serialized JSON in a text content block for backwards
compatibility, and lets tools declare an `outputSchema` that clients validate against.
The 2026-07-28 release candidate keeps this direction and lifts tool schemas to full
JSON Schema 2020-12. Consequently, an additive response field should appear in both
channels, response-size budgets must count both channels, and the tool's output schema
should describe the new field.

Sources:

- [MCP specification, "Tools" (2025-06-18)](https://modelcontextprotocol.io/specification/2025-06-18/server/tools).
- [MCP blog, "2026-07-28 release candidate" (2026-05-21)](https://blog.modelcontextprotocol.io/posts/2026-07-28-release-candidate/).

### Treat summarized documents as a prompt-injection surface

OWASP's GenAI risk LLM01 (prompt injection) explicitly covers instructions hidden in
retrieved or ingested documents. Recommended mitigations for a summarization pipeline:
constrain the summarizer's role, segregate and mark untrusted document text, validate
output against a rigid schema, and give the summarizer no tools or privileges.

Source: [OWASP GenAI, "LLM01: Prompt Injection" (2025)](https://genai.owasp.org/llmrisk/llm01-prompt-injection/).

### Do not trust a pinned judge to be deterministic

LLM-as-judge protocols should pin the judge model, prompt, and temperature, but pinned
seeds still do not guarantee deterministic scores because of server-side batching
nondeterminism. Protocols therefore need repeated runs with reported variance, and the
judge should be calibrated against human labels (approximately 0.8 agreement is the
commonly cited bar) before judge-only gating.

Sources:

- [Thinking Machines, "Defeating nondeterminism in LLM inference" (2025)](https://thinkingmachines.ai/blog/defeating-nondeterminism-in-llm-inference/).
- [RAGAS faithfulness metric documentation (accessed 2026-07-11)](https://docs.ragas.io/en/stable/concepts/metrics/available_metrics/faithfulness/).

### Choose the summary language by its consumer, not by habit

Research on multilingual RAG finds cross-lingual retrieval degrades markedly and that
generators prefer context in the query's language. That applies when summaries feed
retrieval or generation in the document's language; a display-only summary consumed by
agents can standardize on one language, with a configurable language as a deliberate
later extension.

Source: [arXiv 2502.11175, "Multilingual retrieval-augmented generation" (2025)](https://arxiv.org/abs/2502.11175).

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
- Treat source files as untrusted data, not instructions, during summary generation
  (OWASP LLM01).
- Preserve the MCP dual-channel result shape, budget response size for both channels,
  and describe the new field in the tool's `outputSchema`.
- Evaluate with a pinned judge, repeated runs with reported variance, and a
  demonstrated judge-human agreement bar before judge-only gating.
- Track readiness, latency, retries, failures, stale-generation suppression, and
  backfill progress without logging file content or summaries.
