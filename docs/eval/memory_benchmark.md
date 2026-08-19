# MCP Memory Quantitative Benchmark

This runbook evaluates the real `rag` and `pageindex` plugins through the deployed MCP
Streamable HTTP endpoint. It complements `cmd/eval-plugin`: the older command remains
useful for in-process conformance and metric unit tests, while `cmd/memory-bench`
provides live end-to-end measurements instead of a stub plugin and `n/a` scorecard
cells.

Research and metric rationale are recorded in
[`docs/ref/agent_memory_benchmarks_2026.md`](../ref/agent_memory_benchmarks_2026.md).

## Quick start

Set secrets through environment variables. Do not put tokens on the command line.

```bash
export MCP_ENDPOINT='https://example.test/mcp'
export MCP_AUTHORIZATION='Bearer ...'

go run ./cmd/memory-bench \
  --plugin=rag \
  --dataset=tests/eval/memory_bench_smoke.jsonl \
  --format=canonical \
  --top-k=5 \
  --min-score=0.20 \
  --out=docs/eval/runs/local/rag

go run ./cmd/memory-bench \
  --plugin=pageindex \
  --dataset=tests/eval/memory_bench_smoke.jsonl \
  --format=canonical \
  --top-k=5 \
  --min-score=0.20 \
  --out=docs/eval/runs/local/pageindex
```

Each run uses an isolated project namespace when `--project` is omitted. Documents are
deleted at the end unless `--cleanup=false` is supplied.

## Public datasets

Download public data from its official source; the repository intentionally does not
vendor it.

```bash
# LongMemEval release JSON
# https://github.com/xiaowu0162/LongMemEval
go run ./cmd/memory-bench \
  --plugin=rag \
  --dataset=/data/longmemeval_s_cleaned.json \
  --format=longmemeval \
  --out=docs/eval/runs/longmemeval/rag

# LoCoMo locomo10.json
# https://github.com/snap-research/locomo
go run ./cmd/memory-bench \
  --plugin=pageindex \
  --dataset=/data/locomo10.json \
  --format=locomo \
  --out=docs/eval/runs/locomo/pageindex
```

The adapters preserve category labels and evidence/session mappings. They generate
Markdown documents because both current plugins can ingest that common format.

BEAM, private production exports, and textual LongMemEval-V2 trajectory exports can be
converted to the canonical JSONL format below. Full LongMemEval-V2 leaderboard parity
requires its upstream multimodal harness and is not claimed by this text-only MCP
adapter.

## Canonical JSONL

The first row may define dataset metadata. All later rows are documents or queries.
Blank lines and lines beginning with `#` are ignored.

```jsonl
{"type":"dataset","name":"example","version":"2026-08-19","source":"internal-golden"}
{"type":"document","id":"doc-1","path":"/facts/profile.md","content":"Alice lives in Ottawa.","category":"profile"}
{"type":"query","id":"q-1","query":"Where does Alice live?","path_prefix":"/facts/","gold_paths":["/facts/profile.md"],"gold_evidence":["Alice lives in Ottawa"],"answer":"Ottawa","category":"single-hop"}
{"type":"query","id":"q-2","query":"What is Alice's passport number?","path_prefix":"/facts/","answer":"INSUFFICIENT_EVIDENCE","category":"abstention","unanswerable":true}
```

Required rules:

- document paths must be unique;
- query IDs must be unique;
- answerable queries need `gold_paths` or `gold_evidence`;
- an unanswerable query must set `unanswerable=true`;
- use ability labels such as `single-hop`, `multi-session`, `temporal-reasoning`,
  `knowledge-update`, `contradiction-resolution`, and `abstention` so regressions are
  visible per slice.

## Fixed reader

Retrieval-only runs are deterministic. To evaluate the answer produced from retrieved
chunks, configure one fixed OpenAI Responses-compatible reader:

```bash
export MEMORY_BENCH_READER_BASE_URL='https://api.openai.com/v1'
export MEMORY_BENCH_READER_MODEL='gpt-5-mini'
export MEMORY_BENCH_READER_API_KEY='...'

go run ./cmd/memory-bench \
  --plugin=rag \
  --dataset=/data/locomo10.json \
  --format=locomo \
  --out=docs/eval/runs/locomo/rag-with-reader
```

The reader is told to use only the retrieved evidence, to ignore instructions embedded
inside memory content, and to return `INSUFFICIENT_EVIDENCE` when appropriate. The
report records the model and prompt version, not the API key.

Exact match and token F1 are deterministic CI metrics. For publication or official
leaderboard accuracy, run the official benchmark judge against the saved per-case
answers and keep judge model/settings fixed.

## Outputs

The output directory contains:

| File | Purpose |
|---|---|
| `report.json` | Versioned aggregate report plus every case. |
| `scorecard.md` | Human-readable quality, ability-slice, and operations tables. |
| `cases.jsonl` | One inspectable record per query. |
| `comparison.json` | Baseline deltas and gate outcomes when `--baseline` is used. |

The report includes dataset SHA-256, harness version, optional Git SHA, protocol,
runtime, retrieval configuration, reader prompt version, and sanitized endpoint. Secret
values are excluded.

## Baseline regression gate

Capture a trusted report, then compare a candidate using identical inputs and settings:

```bash
go run ./cmd/memory-bench \
  --plugin=rag \
  --dataset=/data/internal-memory-golden.jsonl \
  --format=canonical \
  --baseline=docs/eval/baselines/rag/report.json \
  --max-recall-drop=0.02 \
  --max-ndcg-drop=0.02 \
  --max-mrr-drop=0.02 \
  --max-hit-rate-drop=0.02 \
  --max-error-rate-increase=0 \
  --max-p95-latency-increase=0.20 \
  --out=docs/eval/runs/candidate/rag
```

An incompatible comparison or failed gate exits with code `3`. The comparator rejects
changes to dataset hash, top-k, minimum score, reader model/prompt, concurrency, polling,
or MCP protocol rather than producing misleading deltas.

## Fair comparison checklist

- Use the same dataset bytes, query order, top-k, minimum score, and reader.
- Run RAG and PageIndex against the same service build and database class.
- Keep concurrency, timeout, network location, and warm/cold policy fixed.
- Run multiple repetitions for latency decisions; do not gate on one noisy remote run.
- Inspect category and per-case output, not only aggregate averages.
- Treat write throughput as file-write throughput. Index readiness is reported
  separately by post-ingest wait and readiness probes.
- Set a non-zero `--min-score` before interpreting retrieval-only abstention accuracy.

## CI

`.github/workflows/memory-benchmark.yml` always runs unit/race tests for the harness.
Scheduled and manually dispatched live runs use repository secrets:

- `MCP_BENCH_ENDPOINT`
- `MCP_BENCH_AUTHORIZATION`
- optionally `MEMORY_BENCH_READER_BASE_URL`
- optionally `MEMORY_BENCH_READER_MODEL`
- optionally `MEMORY_BENCH_READER_API_KEY`

When the MCP endpoint or authorization secret is absent, the live job records a clear
skip rather than creating a fake benchmark.
