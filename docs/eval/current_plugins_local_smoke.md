# Current MCP Memory Plugin Local Smoke Benchmark

**Captured:** 2026-08-20T02:07:12.788718Z  
**Commit:** `0540bf4f11c5b5c0628d675a8a9b57d1105a9481`  
**Dataset:** `mcp-memory-smoke` / `16ee655d76bb002fe584f31ba8236333a7a2c855457407dc535ac7cd1642268b`

## Method

- The same runner and dataset are used for both plugins.
- Backend: real in-process production plugin over an isolated SQLite file service.
- RAG: production RAG plugin and file-service lexical/raw fallback.
- PageIndex: production PageIndex plugin, Markdown indexer, SystemFS tree persistence, and tree-search loop with deterministic StubLLM.
- Settings: top-k 5, min-score 0.20, concurrency 1, one warm-up, three timed repetitions, seed 42.
- Four documents and six queries are evaluated: five answerable ability cases and one unanswerable retrieval-abstention case.
- Local latency is diagnostic only; deployed MCP results are required for production SLO decisions.
- The reports have `status=valid`, zero failed cases, and zero execution error rate, so they are eligible regression baselines.
- Baseline replacement is an explicit manual action; ordinary pull requests only compare candidates with these committed reports.

## Results

| Plugin | Status | Failed | Recall@5 | Precision@5 | nDCG@5 | MRR | Hit@5 | Evidence recall | Retrieval abstention | Unexpected retrieval | Error rate | Search p50 ms | Search p95 ms | Index-ready p95 ms |
|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| rag | valid | 0 | 1.0000 | 0.2400 | 0.9839 | 1.0000 | 1.0000 | 1.0000 | 0.0000 | 1.0000 | 0.0000 | 0.084 | 0.109 | 0.124 |
| pageindex | valid | 0 | 1.0000 | 0.2400 | 0.6772 | 0.5500 | 1.0000 | 0.8000 | 0.0000 | 1.0000 | 0.0000 | 0.465 | 0.872 | 0.591 |

## Interpretation

Both plugins retrieved every labelled relevant document within the top five on this fixture: Recall@5 and Hit@5 are 1.0 for both. RAG ranked the relevant document first in every answerable case, producing MRR 1.0 and nDCG@5 0.9839. PageIndex found all relevant documents but ranked them lower in several cases, producing MRR 0.55 and nDCG@5 0.6772. Its evidence recall of 0.8 reflects one long-document case where the correct document was retrieved but the exact labelled evidence string was outside the returned top-five chunks under the deterministic traversal.

Precision@5 is 0.24 for both because the metric uses a fixed denominator of five while most cases have one relevant document and the multi-hop case has two. It should be interpreted together with Recall, MRR, and nDCG rather than as a standalone failure.

The fixed answer reader is disabled. Therefore the final two quality columns measure retrieval behavior, not LLM hallucination: both plugins returned at least one above-threshold candidate for the synthetic unknown query, yielding retrieval-abstention accuracy 0 and unexpected-retrieval rate 1. This is a valid measured limitation of the current `min-score=0.20` retrieval policy, not an answer-generation result.

These numbers are regression-smoke baselines for the exact repository fixture, not public benchmark leaderboard claims. PageIndex uses a deterministic StubLLM so the local run measures production persistence, tree construction, traversal, ranking, and result shaping without provider variance; it does not estimate production reasoning-model quality or latency. The scheduled `mcp` backend job remains the authoritative end-to-end operational measurement.
