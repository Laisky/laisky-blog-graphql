# file_summary_v1 evaluation harness

This directory freezes the reproducible evaluation for the file-level summary contract
(`docs/proposals/file_search_file_summaries.md` §8.6). It gates **A2** (summary is
bounded and useful) and **Q06/Q11/Q13** before the response-enforcement gate
(`settings.mcp.tools.memory.plugins.rag.search.enforce_summary`) is turned on.

## What is frozen here

| File | Purpose | Status |
| --- | --- | --- |
| `rubric.md` | Claim-groundedness, key-topic coverage, critical-error, and degraded-fallback scoring rules. | Frozen. |
| `judge_config.yml` | Immutable judge model id, prompt hash, temperature, seed, and thresholds. | Frozen. |
| `corpus.jsonl` | ≥200 labeled files stratified across length, text/code, Markdown/PDF, source language, empty/opaque, and injection cases. | **Seed only — operators must complete to ≥200 rows before gating.** |
| `raw_results.jsonl` | Per-file generated summary + per-metric judge scores. | Produced by a run. |
| `scorecard.md` | Aggregated means, spread across ≥3 judge runs, pass/fail vs thresholds. | Produced by a run. |
| `run_metadata.yml` | Generator model/prompt/limits, token/call counts, code revision, dependency versions. | Produced by a run. |
| `human_audit.md` | Stratified 50-file audit by two reviewers with adjudicated disagreements. | Operator/reviewer responsibility. |

## Why the run is not committed here yet

The engineering slice of the proposal has landed and is covered by unit/integration
tests under `internal/mcp/files/` and `internal/mcp/memory/plugins/pageindex/`
(deterministic contract, mutation, failure, isolation, and budget behavior). The full
golden-corpus run requires:

- a live, pinned LLM judge (server-side batching means a pinned seed is not fully
  deterministic — the protocol runs the judge **≥3 times** and reports per-metric mean
  and spread; a threshold pass must hold on the mean with the worst run within 2
  points); and
- a two-reviewer human audit whose agreement with the judge must be **≥0.80** before
  judge scores may gate; below that, human adjudication is authoritative.

Neither can run inside the code CI sandbox, so they are operator/reviewer
responsibilities recorded here rather than fabricated. A floating judge alias or an
undocumented prompt change invalidates a run.

## How to run (operators)

1. Complete `corpus.jsonl` to the ≥200-file stratified target described in `rubric.md`.
2. Generate summaries for each corpus file through the real RAG/PageIndex producers at
   a pinned generator model/prompt/limits; write `raw_results.jsonl` and
   `run_metadata.yml`.
3. Score with the judge in `judge_config.yml`, ≥3 repeats; write `scorecard.md`.
4. Complete `human_audit.md` (50-file stratified, two reviewers).
5. Gate: mean groundedness ≥ 0.90, key-topic coverage ≥ 0.80, zero critical unsupported
   claims, fallback-subset mean key-topic coverage ≥ 0.50, and no retrieval regression
   on the frozen retrieval corpus.

Any change to the generator model, prompt, or effective limits changes
`summary_generation_key` and must trigger a new run.
