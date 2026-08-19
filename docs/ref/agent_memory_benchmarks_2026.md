# Agent Memory Evaluation in 2026

**Status:** current research note  
**Last reviewed:** 2026-08-19  
**Applies to:** MCP `rag` and `pageindex` memory plugins

## Decision

Evaluate memory as a multi-axis system rather than publishing one opaque score. A
production scorecard must keep retrieval quality, answer quality, abstention,
freshness, latency, and context cost separate. A plugin may improve one axis while
regressing another, and that trade-off must remain visible.

The in-tree runner therefore uses the public MCP `file_*` surface. This includes
transport, plugin selection, writes, indexing, retrieval, and deletion in the
measurement instead of benchmarking a private helper that production callers do not
use.

## Benchmark landscape

### LongMemEval

LongMemEval remains the stable conversational-memory benchmark. Its released set has
500 questions spanning single-session user facts, assistant facts, preferences,
multi-session synthesis, temporal reasoning, and knowledge updates. It is useful for
regression tests because the question categories map directly to concrete memory
abilities and the official data includes evidence-session labels.

Source: <https://github.com/xiaowu0162/LongMemEval>

### LoCoMo

LoCoMo supplies ten unusually long, multi-session conversations with annotated QA,
evidence dialog IDs, timestamps, and event summaries. It is particularly useful for
single-hop, multi-hop, temporal, and open-domain conversational recall. The native
adapter in this repository converts each session into one Markdown memory document
and preserves the evidence dialog text for evidence-recall scoring.

Source: <https://github.com/snap-research/locomo>

### BEAM

BEAM extends evaluation to larger histories and ability-specific slices such as
preference following, instruction following, information extraction, knowledge
updates, temporal reasoning, event ordering, contradiction resolution, summarization,
and abstention. The open Mem0 benchmark suite exposes LoCoMo, LongMemEval, and BEAM
through a common ingest-search-evaluate pipeline.

Source: <https://github.com/mem0ai/memory-benchmarks>

### LongMemEval-V2

LongMemEval-V2 moves from chat history to long histories of multimodal agent
trajectories. It evaluates answer accuracy and query latency over 451 manually curated
questions, five agent-memory abilities, two domains, and haystacks that can reach 115M
tokens. This makes latency and compact evidence first-class benchmark outputs rather
than secondary telemetry.

The current MCP plugins expose a text/PDF file surface, so this PR does not claim full
leaderboard parity for LongMemEval-V2's screenshot-bearing trajectories. Textual
trajectory exports can be converted to the canonical JSONL contract, while an official
leaderboard submission should use the upstream harness and preserve its multimodal
inputs.

Source: <https://github.com/xiaowu0162/LongMemEval-V2>

## 2026 system-design signals

Recent work reinforces that accuracy alone is insufficient:

- **MemForest** evaluates quality together with memory-construction throughput and
  freshness latency, motivated by expensive sequential write paths.
- **LazyMem** reports answer quality together with retrieved-memory tokens and latency,
  showing that broad retrieval can preserve recall while selective query-time
  construction controls noise.
- **LycheeMemory V2** reports construction-token reductions alongside LongMemEval and
  LoCoMo quality, emphasizing consolidation granularity as a cost/quality decision.

Sources:

- <https://arxiv.org/abs/2605.23986>
- <https://arxiv.org/abs/2607.22690>
- <https://arxiv.org/abs/2608.12990>

## Metric contract

### Retrieval

The runner uses standard information-retrieval metrics also exposed by BEIR:

- `Recall@k`: fraction of unique relevant documents retrieved.
- `Precision@k`: relevant unique documents divided by `k`.
- `nDCG@k`: rank-sensitive binary relevance.
- `MRR`: reciprocal rank of the first relevant document.
- `Hit rate@k`: fraction of queries with at least one relevant result.
- `Evidence recall`: fraction of labelled evidence strings present in returned chunks.

Source: <https://github.com/beir-cellar/beir>

Document metrics deduplicate repeated chunks from the same file. The returned chunks
are not discarded: evidence recall, the optional reader, and context-token estimates
still use the actual top-k chunk payload.

### End-to-end answers

A fixed optional reader receives only the retrieved evidence and must emit
`INSUFFICIENT_EVIDENCE` when evidence is missing. The default deterministic metrics
are normalized exact match and token F1. These are suitable for CI because they do not
introduce judge variance.

For semantic or leaderboard accuracy, keep the answerer and judge models fixed and run
the official benchmark judge or the repository's existing RAGAS-oriented evaluation.
Judge model, prompt, and retrieval depth materially affect scores and must not change
between compared runs.

RAGAS source: <https://github.com/vibrantlabsai/ragas>

### Abstention and readiness

An unanswerable query is not credited merely because asynchronous indexing has not
finished. Before the real abstention query, the runner uses a labelled document from
the same path scope as an index-readiness probe. A timeout becomes an error rather
than a false abstention success. Meaningful retrieval-only abstention also requires a
fixed `min_score` threshold.

### Operations

Every report includes:

- file-write latency and write throughput;
- final search latency p50/p95/p99;
- post-ingest wait-to-relevant latency;
- readiness-probe latency for unanswerable cases;
- estimated retrieved-context tokens;
- provider-reported reader tokens when a reader is enabled;
- per-query errors and attempts.

## Reproducibility rules

A comparison is valid only when the following remain equal:

- dataset SHA-256;
- report schema and reader prompt version;
- top-k and minimum score;
- concurrency and polling configuration;
- MCP protocol version;
- reader model.

For operational comparisons, use the same deployed build, hardware class, database
state, network location, and warm/cold policy. Change one variable at a time. Store the
raw per-case output; averages alone hide regressions in important ability slices.

## Dataset policy

Public benchmark data is not vendored into this repository. It can be large, may have
separate terms, and changes independently. The runner records the exact input hash,
so downloaded datasets remain reproducible without committing them. CI uses only the
small synthetic smoke fixture under `tests/eval/`.
