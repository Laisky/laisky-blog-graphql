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
- Local latency is diagnostic only; deployed MCP results are required for production SLO decisions.
- The first validated capture seeds immutable local baseline reports under `docs/eval/baselines/local/`; later PR runs compare against them.

## Results

| Plugin | Status | Failed | Recall@5 | Precision@5 | nDCG@5 | MRR | Hit@5 | Evidence recall | Retrieval abstention | Error rate | Search p50 ms | Search p95 ms | Index-ready p95 ms |
|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| rag | valid | 0 | 1.0000 | 0.2400 | 0.9839 | 1.0000 | 1.0000 | 1.0000 | 0.0000 | 0.0000 | 0.084 | 0.109 | 0.124 |
| pageindex | valid | 0 | 1.0000 | 0.2400 | 0.6772 | 0.5500 | 1.0000 | 0.8000 | 0.0000 | 0.0000 | 0.465 | 0.872 | 0.591 |

## Interpretation

These numbers are valid regression-smoke baselines for the exact repository fixture, not public benchmark leaderboard claims. PageIndex uses a deterministic StubLLM so the local run measures the production persistence, tree construction, traversal, ranking, and result-shaping code without provider variance; it does not estimate the quality or latency of a production reasoning model. The scheduled `mcp` backend job is the authoritative end-to-end operational measurement because it includes transport, authentication, production storage, indexing workers, and configured model providers. That deployed job is intentionally score-only until an operator adopts a stable environment-specific production baseline.
