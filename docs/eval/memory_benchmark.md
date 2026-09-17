# MCP Memory Quantitative Benchmark

This runbook evaluates the current `rag` and `pageindex` plugins with one shared
scorecard. It complements `cmd/eval-plugin`: the older command is useful for
conformance and RAGAS-oriented metric tests, while `cmd/memory-bench` performs corpus
write, index-readiness, retrieval, optional answer generation, artifact creation, and
regression comparison.

Research and metric rationale are recorded in
[`docs/ref/agent_memory_benchmarks_2026.md`](../ref/agent_memory_benchmarks_2026.md).
The latest validated deterministic baselines are recorded in
[`docs/eval/current_plugins_local_smoke.md`](current_plugins_local_smoke.md).

## Execution modes

### Deployed MCP — authoritative

This mode calls the public MCP Streamable HTTP endpoint. It includes authentication,
transport, plugin routing, storage, indexing, and retrieval.

```bash
export MCP_ENDPOINT='https://example.test/mcp'
export MCP_AUTHORIZATION='Bearer ...'

go run ./cmd/memory-bench \
  --backend=mcp \
  --plugin=rag \
  --dataset=tests/eval/memory_bench_smoke.jsonl \
  --format=canonical \
  --top-k=5 \
  --min-score=0.20 \
  --warmup=2 \
  --repetitions=10 \
  --out=docs/eval/runs/live/rag
```

Run the same command with `--plugin=pageindex` and otherwise identical settings for a
fair comparison.

### Local current-plugin smoke — deterministic, opt-in

This mode constructs the real Go plugins against an isolated SQLite-backed file
service:

- RAG uses `plugins/rag.Plugin` and the production file service's lexical/raw fallback;
- PageIndex uses `plugins/pageindex.Plugin`, the real Markdown indexer, private
  `SystemFS`, tree persistence, and search loop; external model variance is removed by
  injecting the repository's deterministic `StubLLM`.

```bash
make memory-bench-current
```

Equivalent direct commands:

```bash
go run ./cmd/memory-bench \
  --backend=local \
  --plugin=rag \
  --dataset=tests/eval/memory_bench_smoke.jsonl \
  --format=canonical \
  --top-k=5 \
  --min-score=0.20 \
  --concurrency=1 \
  --warmup=1 \
  --repetitions=3 \
  --seed=42 \
  --out=docs/eval/runs/local/rag

go run ./cmd/memory-bench \
  --backend=local \
  --plugin=pageindex \
  --dataset=tests/eval/memory_bench_smoke.jsonl \
  --format=canonical \
  --top-k=5 \
  --min-score=0.20 \
  --concurrency=1 \
  --warmup=1 \
  --repetitions=3 \
  --seed=42 \
  --out=docs/eval/runs/local/pageindex
```

Local quality results are valid regression evidence for the exact deterministic
fixture. Local latency is diagnostic only and must not be compared with production
SLOs. PageIndex's local `StubLLM` makes tree traversal reproducible; it does not model
the quality or latency of the production reasoning model.

## What is measured

### Retrieval

- unique-document Recall@k;
- Precision@k;
- binary nDCG@k;
- MRR;
- Hit rate@k;
- labelled evidence-string recall over returned chunk text.

Repeated chunks from the same file count once for document metrics but remain in the
reader context and evidence calculation.

### Answers and abstention

An optional fixed Responses-compatible reader is restricted to returned evidence and
must emit `INSUFFICIENT_EVIDENCE` when the evidence is insufficient. The runner reports
normalized exact match, token precision/recall/F1, rubric coverage, answer abstention
accuracy, and false-answer rate.

```bash
export MEMORY_BENCH_READER_BASE_URL='https://api.openai.com/v1'
export MEMORY_BENCH_READER_MODEL='gpt-5-mini'
export MEMORY_BENCH_READER_API_KEY='...'
```

When the reader is disabled, the harness does not claim to measure hallucination or
answer quality. It reports **retrieval abstention accuracy** and **unexpected retrieval
rate** instead: an unanswerable case passes only when no hit survives the configured
`--min-score` threshold. Choose and version that threshold before comparing runs.

The reader prompt version and model are stored in the report. For an official public
benchmark result, run the upstream evaluator or judge against `cases.jsonl`; do not
present the deterministic CI scorer as leaderboard parity.

### Operations

- `file_write` mean and p50/p95/p99;
- documents/second;
- write-to-relevant index readiness latency;
- repeated `file_search` mean and p50/p95/p99;
- retrieved-context token estimate;
- fixed-reader provider token usage;
- per-case attempts and errors.

## Validity and failure semantics

A numeric zero is a measurement only when the case completed successfully. The harness
uses the following rules to prevent execution failures from being misread as poor
quality:

1. Any write, readiness, search, reader, or cleanup failure is recorded on the case.
2. Failed cases are excluded from aggregate quality denominators and appear as `n/a`
   in the scorecard instead of `0.0000`.
3. A report with any failed case has `status: invalid`; retrieval or answer abstention
   credit is disabled for the whole run.
4. Diagnostic `report.json`, `cases.jsonl`, and `scorecard.md` are still written, then
   the command exits non-zero.
5. Invalid reports are rejected as both baselines and candidates by the comparison
   gate. CI refuses to capture them under `docs/eval/baselines/`.

This contract specifically prevents an empty or broken index from receiving a perfect
abstention score.

## Supported datasets

### Canonical JSONL

```jsonl
{"type":"dataset","name":"example","version":"2026-08-19","source":"internal-golden"}
{"type":"document","id":"doc-1","path":"/facts/profile.md","content":"Alice lives in Ottawa."}
{"type":"query","id":"q-1","query":"Where does Alice live?","gold_paths":["/facts/profile.md"],"gold_evidence":["Alice lives in Ottawa"],"answer":"Ottawa","category":"single-hop"}
{"type":"query","id":"q-2","query":"What is the ZXQ-991 launch code?","answer":"INSUFFICIENT_EVIDENCE","category":"abstention","unanswerable":true}
```

Document paths and query IDs must be unique. Answerable queries require at least one
gold path or evidence string. Public data can be normalized to this format when no
native adapter is appropriate.

### LongMemEval

```bash
go run ./cmd/memory-bench \
  --backend=mcp \
  --plugin=rag \
  --dataset=/data/longmemeval_s_cleaned.json \
  --format=longmemeval \
  --out=docs/eval/runs/longmemeval/rag
```

Each question receives an isolated path prefix. History sessions become documents,
`answer_session_ids` become gold paths, and `has_answer` turns become gold evidence.

### LoCoMo

```bash
go run ./cmd/memory-bench \
  --backend=mcp \
  --plugin=pageindex \
  --dataset=/data/locomo10.json \
  --format=locomo \
  --out=docs/eval/runs/locomo/pageindex
```

Each conversation session becomes a Markdown document. Evidence dialog IDs are mapped
to their session paths and original text.

### MemoryAgentBench

Use `--format=memoryagentbench` with an array or JSONL export containing `questions`,
`answers`, benchmark metadata, and one of the supported context/chunk fields. Exports
with sample-level labels are marked as sample-level relevance in query metadata.
Official task scores still come from the upstream evaluator.

### BEAM

Point `--dataset` to one scenario directory containing `chat.json` and
`probing_questions/probing_questions.json`, then use `--format=beam`. Source chat IDs
become gold paths/evidence and benchmark rubrics are retained.

Public data is deliberately not vendored. Exact input bytes are identified by SHA-256
in every report.

## Outputs

The output directory contains:

| File | Purpose |
|---|---|
| `report.json` | Versioned status, aggregates, metadata, categories, and all cases. |
| `scorecard.md` | Human-readable validity, quality, slice, operations, and case tables. |
| `cases.jsonl` | One inspectable record per query for upstream judges and diagnosis. |
| `comparison.json` | Machine-readable baseline deltas and gate outcomes. |
| `comparison.md` | Human-readable regression table and paired statistical test. |

Reports record the dataset hash, Git SHA, backend, plugin, configuration hash, MCP
protocol, runtime, host, warm-up, repetitions, seed, and fixed-reader identity. Secrets,
URL credentials, query strings, and fragments are excluded.

## Baseline regression gate

Capture a trusted report and compare a candidate using identical inputs and settings:

```bash
go run ./cmd/memory-bench \
  --backend=mcp \
  --plugin=rag \
  --dataset=/data/internal-memory-golden.jsonl \
  --format=canonical \
  --baseline=docs/eval/baselines/rag/report.json \
  --max-recall-drop=0.02 \
  --max-ndcg-drop=0.02 \
  --max-mrr-drop=0.02 \
  --max-hit-rate-drop=0.02 \
  --max-evidence-recall-drop=0.02 \
  --max-error-rate-increase=0 \
  --max-p95-latency-increase=0.20 \
  --permutation-iterations=10000 \
  --permutation-alpha=0.05 \
  --out=docs/eval/runs/candidate/rag
```

Incompatible inputs, invalid reports, or a failed gate exit with code `3` or a general
non-zero execution code. Compatibility requires the same report schema, dataset hash,
plugin, query count, and configuration hash. The comparison also reports a paired
two-sided sign-flip permutation test over per-query nDCG differences.

## Fair-comparison checklist

- Use identical dataset bytes, top-k, minimum score, answerer, and prompt.
- Run both plugins against the same build, database class, network location, and load.
- Keep concurrency, warm-up, repetitions, timeout, and cold/warm policy fixed.
- Use multiple repetitions for latency; never gate on one remote call.
- Inspect ability slices and failed cases, not only aggregate averages.
- Set a validated minimum score before interpreting retrieval-only abstention.
- Separate local deterministic results from deployed production results.

## CI — optional manual benchmarks

The following evaluation workflows use **only `workflow_dispatch`**. Opening or
updating a PR does not run them, and their former nightly schedules are removed.
Benchmark commands, datasets, comparison thresholds, and baselines are unchanged.

| Workflow | Manual behavior |
|---|---|
| `memory-benchmark.yml` | Runs its existing unit/race/coverage/vet job and local RAG/PageIndex regression gates. Deployed MCP jobs run only when `run_live=true` (default: false). |
| `memory-benchmark-results.yml` | Runs both local plugins and uploads exact reports. Leave `pr_number` blank for artifacts only; set it to publish to that PR. |
| `memory-benchmark-capture.yml` | Retains its existing manual capture/commit behavior. Replacing existing baselines still requires `replace_baselines=true`. |
| `eval-nightly.yml` | Runs the existing evaluation harness manually and uploads scorecards. The filename/name is historical; there is no nightly schedule or automatic PR comment. |

No new workflow or job is introduced. The benchmark workflow's bundled unit job is
also manual; it was not moved into another automatic workflow. Independent `ci`,
`lint`, and CodeQL configurations are unchanged, including the existing full test
suite in `lint.yml`. Test sources are not deleted or disabled.

In GitHub, open **Actions**, select the workflow, choose **Run workflow**, and select
the branch to test. To publish a result to a PR, select that PR's head branch in this
repository and set `pr_number`. Publishing checks that the report's actual Git SHA
matches the PR's current head; a wrong branch, outdated head, invalid PR number, or
fork head is rejected instead of attaching a misleading scorecard. Only matching
`github-actions[bot]` comments are updated. Reports remain available as artifacts.

Examples after the dispatch configuration is available on the default branch:

```bash
# Local tests and regression gates; no deployed endpoint calls.
gh workflow run memory-benchmark.yml --ref YOUR_BRANCH

# Explicitly opt into deployed MCP measurements as well.
gh workflow run memory-benchmark.yml --ref YOUR_BRANCH -f run_live=true

# Test the current PR head branch and publish its scorecards.
gh workflow run memory-benchmark-results.yml --ref YOUR_PR_BRANCH -f pr_number=45

# Evaluation only; artifacts are retained, with no automatic PR comments.
gh workflow run eval-nightly.yml --ref YOUR_BRANCH
```

GitHub requires a dispatchable workflow on the default branch for manual invocation;
merge these trigger changes before relying on the new results-workflow dispatch UI.
Default-branch schedules retain their old behavior until the changes are merged.
See [GitHub's manual workflow instructions](https://docs.github.com/en/actions/how-tos/manage-workflow-runs/manually-run-a-workflow).

Explicit deployed runs require `MCP_BENCH_ENDPOINT` and `MCP_BENCH_AUTHORIZATION`.
The optional reader uses `MEMORY_BENCH_READER_BASE_URL`,
`MEMORY_BENCH_READER_MODEL`, and `MEMORY_BENCH_READER_API_KEY`. Missing deployed
secrets produce an explicit skip; CI never substitutes fake production scores.
