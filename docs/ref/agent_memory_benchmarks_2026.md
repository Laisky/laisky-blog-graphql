# Agent Memory Evaluation in 2026

**Status:** current research note  
**Last reviewed:** 2026-08-19  
**Applies to:** MCP `rag` and `pageindex` memory plugins

## Decision

Evaluate memory as a multi-axis system rather than publishing one opaque score. A
production scorecard must keep retrieval quality, end-to-end answer quality,
abstention, freshness, latency, throughput, and retrieved-context cost separate. A
plugin may improve one axis while regressing another; that trade-off must remain
visible.

The in-tree runner therefore uses the same `file_write`, `file_search`, and
`file_delete` contract used by production callers. It has two execution modes:

1. `mcp`: the authoritative deployed test. It includes HTTP transport,
   authentication, plugin routing, storage, indexing, and retrieval.
2. `local`: a deterministic CI smoke test over the real Go plugin implementations.
   RAG uses the real file service with its lexical/raw fallback and PageIndex uses the
   real indexer, system namespace, and tree search with a deterministic `StubLLM`.
   This mode detects code regressions but is not presented as a public leaderboard
   result.

## Benchmark landscape

### MemoryAgentBench — ICLR 2026

MemoryAgentBench evaluates memory in incremental multi-turn interactions across four
competencies:

- Accurate Retrieval;
- Test-Time Learning;
- Long-Range Understanding;
- Conflict Resolution.

The official implementation maps tasks to exact match, substring exact match,
Recall@5, or an LLM judge depending on the source dataset. It is useful because it
separates abilities that a single long-conversation QA score can hide. The repository
also follows an inject-once/query-many design that is efficient for repeated plugin
experiments.

Sources:

- <https://github.com/HUST-AI-HYZ/MemoryAgentBench>
- <https://arxiv.org/abs/2507.05257>
- <https://openreview.net/forum?id=DT7JyQC3MR>

The native adapter accepts JSON or JSONL exports containing `questions`, `answers`, a
context/chunk field, and the benchmark metadata arrays. When an export only provides
sample-level context labels, retrieval relevance is intentionally recorded at sample
level; official task scoring should still use the upstream evaluator.

### LongMemEval

LongMemEval remains the stable conversational-memory regression benchmark. Its
released cleaned set has 500 questions covering:

- information extraction;
- multi-session reasoning;
- knowledge updates;
- temporal reasoning;
- abstention.

The official data includes evidence session IDs and turn-level `has_answer` labels.
The adapter preserves both: session IDs become gold document paths and labelled turns
become gold evidence strings.

Sources:

- <https://github.com/xiaowu0162/LongMemEval>
- <https://arxiv.org/abs/2410.10813>

### LongMemEval-V2

LongMemEval-V2 extends the problem from chat transcripts to long histories of agent
trajectories and makes query latency a first-class output. Its multimodal trajectories
can include screenshots and much larger histories. The current MCP memory surface is
text/PDF oriented, so this repository does not claim full leaderboard parity for V2.
Textual trajectory exports may be converted to canonical JSONL; official V2
submissions must retain upstream multimodal inputs and evaluation.

Source: <https://github.com/xiaowu0162/LongMemEval-V2>

### LoCoMo

LoCoMo provides ten very long, multi-session conversations with timestamps, dialog
IDs, QA labels, and evidence dialog IDs. It is especially useful for single-hop,
multi-hop, temporal, and open-domain conversational recall. The adapter converts each
session into one Markdown memory document, maps evidence dialog IDs to gold paths, and
preserves evidence text for evidence-recall scoring.

Sources:

- <https://github.com/snap-research/locomo>
- <https://arxiv.org/abs/2402.17753>

### BEAM

BEAM targets large conversational histories and ability-specific probes including
preference following, instruction following, information extraction, knowledge
updates, temporal reasoning, event ordering, contradiction resolution,
summarization, and abstention. The adapter reads one official scenario directory:
`chat.json` plus `probing_questions/probing_questions.json`. Source chat IDs are mapped
to gold document paths and text evidence; rubrics are retained for answer inspection.

Source: <https://github.com/mohammadtavakoli78/BEAM>

### RAGAS and standard retrieval metrics

RAGAS separates retrieval/context quality from answer faithfulness and correctness.
This repository already contains a pure-Go RAGAS-oriented evaluator under
`internal/mcp/memory/conformance/eval`. The new runner complements it with a
production-path retrieval benchmark and deterministic answer metrics.

The retrieval scorecard follows standard information-retrieval definitions used by
BEIR-style evaluation:

- `Recall@k`: fraction of unique relevant documents retrieved;
- `Precision@k`: relevant unique documents divided by `k`;
- `nDCG@k`: binary relevance with logarithmic rank discount;
- `MRR`: reciprocal rank of the first relevant document;
- `Hit rate@k`: fraction of answerable queries with at least one relevant result;
- `Evidence recall`: fraction of labelled evidence strings present in returned chunks.

Sources:

- <https://github.com/vibrantlabsai/ragas>
- <https://github.com/beir-cellar/beir>

Repeated chunks from the same file are deduplicated for document-level metrics but
remain available to the fixed reader and evidence scorer.

## Metric contract

### Retrieval

Document metrics are averaged over answerable queries. Unanswerable questions are
reported separately so they cannot improve retrieval accuracy by contributing empty
gold sets. Evidence recall is measured against actual returned chunk text.

### End-to-end answers

A fixed optional Responses-compatible reader receives only retrieved evidence and is
instructed to return `INSUFFICIENT_EVIDENCE` when evidence is missing. Deterministic
metrics are:

- normalized exact match;
- token precision, recall, and F1;
- rubric substring coverage where a benchmark provides rubrics;
- abstention accuracy and false-answer rate.

For an official public result, use the benchmark's own evaluator or judge against the
saved per-case answers. The answerer model, judge model, prompt version, and top-k must
remain fixed across compared runs.

### Operations

Every report includes:

- `file_write` latency and documents/second;
- write-to-relevant index readiness latency;
- final `file_search` p50/p95/p99 over configurable repetitions;
- retrieved-context token estimate;
- provider-reported fixed-reader tokens;
- errors and attempts for every case.

Latency comparisons require the same service build, hardware/database class, network
location, concurrency, and warm/cold policy. Local CI latency is diagnostic only;
deployed latency is the operational decision input.

### Statistical comparison

Regression gating checks absolute drops in quality metrics, absolute error-rate
increase, and relative p95 latency increase. Candidate and baseline reports are
rejected as incompatible when dataset bytes, plugin, report schema, or benchmark
configuration differ.

A paired two-sided sign-flip permutation test over per-query nDCG differences is also
reported. The default is 10,000 iterations with a fixed seed and alpha 0.05. The test
is informative; configured metric gates remain the merge-blocking contract.

## Reproducibility rules

A valid comparison keeps the following equal:

- dataset SHA-256 and query set;
- report schema and harness configuration hash;
- plugin and backend mode;
- top-k and minimum score;
- concurrency, warm-up, repetitions, polling, and timeouts;
- MCP protocol version;
- fixed reader model and prompt version.

Each run stores raw per-case results, the exact dataset hash, Git SHA, runtime,
architecture, hostname, and a stable configuration hash. Secrets and URL query strings
are never written to artifacts.

## Dataset policy

Public benchmark data is not vendored into this repository. It can be large, may have
separate terms, and evolves independently. The runner hashes the exact input bytes, so
downloaded data remains replayable without committing it. Pull-request CI uses only
the small synthetic fixture under `tests/eval/`; scheduled live jobs may point to a
controlled internal golden set.
