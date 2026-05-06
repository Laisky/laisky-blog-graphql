# MCP Memory Eval Harness Runbook

This is the operator runbook for the pure-Go evaluation harness that produces the
per-plugin scorecards under [./plugin_scorecard.md](plugin_scorecard.md). Use it to run
a scorecard locally, interpret CI output, or capture a new baseline.

## 1. What this is

A pure-Go harness, in-tree, that runs the §7.3 suite from
[../proposals/mcp_memory_plugin_manager.md](../proposals/mcp_memory_plugin_manager.md)
against any plugin implementing the `internal/mcp/memory/plugin.Plugin` interface and
emits the §7.4 scorecard markdown plus per-query JSON.

The harness inherits the proposal §0 constraint: no external language runtime, no
`pip` dependency, no `requirements.txt`, no subprocess, no extra container image. The
RAGAS v0.4 metrics are reimplemented in Go and driven through the same Responses-API
LLM client production uses; prompt fidelity to upstream is asserted via golden fixtures.

## 2. Layout

```
internal/mcp/memory/conformance/eval/
  runner.go              # orchestrator; emits scorecard markdown + per-query JSON
  retrieval.go           # Recall@k, nDCG@10, MRR, Hit@5
  ragas.go               # RAGAS v0.4 metric prompts (line-for-line port)
  redteam.go             # OWASP injection + cross-tenant + supersession + GDPR probes
  ops_probe.go           # latency p50/p95/p99, tokens, throughput, cold-vs-warm
  permutation.go         # paired two-sided permutation test (B = 10 000)
  judge_ensemble.go      # multi-judge consensus for FinanceBench answer grading
  scorecard.go           # markdown writer with deterministic key ordering
  testdata/              # golden RAGAS prompts + canned LLM fixtures
cmd/eval-plugin/
  main.go                # driver binary; constructs the Plugin via the production DI graph
tests/eval/golden/       # LFS-tracked golden datasets (see Section 3)
```

## 3. Running locally

The Make targets are the supported entry points; CI uses the same shell-outs.

```bash
# Per-PR scorecard run for a plugin against tests/eval/golden/.
# Output lands under docs/eval/runs/<sha>/.
make eval-plugin PLUGIN=rag

# Re-capture the frozen Phase-1 baseline (reviewers only — see Section 5).
make eval-baseline-rag
```

Before the first run, pull the LFS-backed golden datasets:

```bash
git lfs pull
```

Required environment variables:

| Variable          | Purpose                                                                                             | Required for                                  |
| ----------------- | --------------------------------------------------------------------------------------------------- | --------------------------------------------- |
| `OPENAI_API_KEY`  | Drives the LLM-judge calls (RAGAS metrics, FinanceBench ensemble) through the production client.    | Any run that exercises generation quality.    |

For deterministic local runs that do not need the LLM judge (retrieval-only smoke
checks), the harness's unit tests use an in-memory fake `LLM`; see
`internal/mcp/memory/conformance/eval/*_test.go`.

## 4. Outputs

| Path                                                  | Contents                                                                                                                            |
| ----------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------- |
| `docs/eval/runs/<sha>/<plugin>_plugin_scorecard.md`   | Per-PR scorecard. Retained 90 days, then garbage-collected.                                                                          |
| `docs/eval/runs/<sha>/raw_per_query.jsonl`            | Per-query metric values backing the scorecard. Retained 90 days.                                                                     |
| `docs/eval/runs/<sha>/run_metadata.yml`               | Git SHA, run UTC, judge models, hardware spec, Go toolchain, embedding model, golden dataset hashes. Required for replay.            |
| `docs/eval/baseline_v1/rag_plugin_scorecard.md`       | Frozen Phase-1 baseline. Committed once; never overwritten without a baseline reset (Section 5).                                     |
| `docs/eval/baseline_v1/raw_per_query.jsonl`           | Frozen raw metric values backing the baseline. Input to the permutation test when a future plugin is compared against `baseline_v1`. |
| `docs/eval/baseline_v1/run_metadata.yml`              | Git SHA, run UTC, judge models, hardware spec, Go toolchain, embedding model, RAGAS-prompt commit pin. Required for replay.          |

## 5. Baseline workflow

The Phase-1 baseline is captured **once** at the Phase-1 ship and committed in the
same PR as the plugin code. Subsequent runs do not overwrite it. A new baseline is
only adopted on a deliberate scorecard reset, which:

1. Requires a reviewer sign-off (typically the same reviewer pair that signed off on
   the original baseline).
2. Requires a `CHANGELOG.md` entry naming the reset, the reason, and the new run's
   git SHA + UTC.
3. Replaces all three files in `docs/eval/baseline_v1/` atomically. Old baselines are
   not retained in the working tree — git history is the audit trail.

This protocol is documented in [../proposals/mcp_memory_plugin_manager.md#75-gating-thresholds](../proposals/mcp_memory_plugin_manager.md#75-gating-thresholds).

## 6. CI

The nightly + per-PR workflow lives at `.github/workflows/eval-nightly.yml`. It
matrixes over the registered plugins and runs `make eval-plugin PLUGIN=<name>` for
each. PRs touching `internal/mcp/memory/plugins/<plugin>/**` or
`tests/eval/golden/**` trigger the workflow on push.

What blocks merge:

- Any §7.5 **hard gate** regression on the touched plugin. The PR status check goes
  red; the comment names the offending metric and the magnitude of the regression.
  Hard gates are listed in
  [../proposals/mcp_memory_plugin_manager.md#75-gating-thresholds](../proposals/mcp_memory_plugin_manager.md#75-gating-thresholds).

What comments without blocking:

- Any **watch** regression. The harness posts a PR comment naming the metric, the
  delta, and the watch threshold. Reviewers decide whether to act.
- Permutation-test result blocks comparing the run against `baseline_v1`.

The statistical gate is a paired two-sided permutation test, B = 10 000 shuffles,
|Q| ≥ 50 per slice (proposal §7.6).

## 7. Adding a plugin

A future plugin author copies the existing plugin's `make eval-plugin PLUGIN=<their_name>`
invocation. The harness composes against the same `plugin.Plugin` interface that
production uses; no harness-side change is required. If the new plugin needs a fixture
(canned LLM responses, deterministic embeddings), drop it under
`internal/mcp/memory/conformance/eval/testdata/<plugin>/` and reference it from the
plugin's own driver in `cmd/eval-plugin/main.go`.

## 7.1 Pageindex evaluation

`pageindex_plugin` ships in Phase 2 and is gated on `OPENAI_API_KEY` (the production
client also reads `OPENAI_BASE_URL` if a non-default endpoint is required). When the
key is configured and the bbolt cache parent directory is writable, drive the
harness with:

```bash
OPENAI_API_KEY=sk-... \
  go run ./cmd/eval-plugin --plugin=pageindex --suites=redteam \
    --out=docs/eval/baseline_v1
```

Required inputs:

| Input                     | Notes                                                                                 |
| ------------------------- | ------------------------------------------------------------------------------------- |
| `OPENAI_API_KEY`          | Drives indexing + retrieval LLM calls. Without it the plugin is unregistered (§6.1).   |
| Writable bbolt cache path | `settings.mcp.tools.memory.plugins.pageindex.indexer.cache.path`; default `/var/lib/laisky/`. |
| `tests/eval/golden/`      | LFS-backed datasets. Until they land, every retrieval/RAGAS/public cell renders `n/a`. |

Driver status: as of this wave, `cmd/eval-plugin` accepts `--plugin=pageindex` only
once the production DI graph wires a real plugin construction (the rag path uses a
stub for the same reason). The Phase-2 placeholder scorecard at
[baseline_v1/pageindex_plugin_scorecard.md](baseline_v1/pageindex_plugin_scorecard.md)
holds the row until then. The full enablement story is in the manual at
[../manual/mcp_memory_plugins.md §6](../manual/mcp_memory_plugins.md#6-pageindex-phase-2).

## 8. References

- Eval harness design — [../proposals/mcp_memory_plugin_manager.md#46-eval-harness-assets-and-baseline_v1-deliverables](../proposals/mcp_memory_plugin_manager.md#46-eval-harness-assets-and-baseline_v1-deliverables)
- Quantitative framework — [../proposals/mcp_memory_plugin_manager.md#7-quantitative-evaluation-framework-2026-industrial-standard](../proposals/mcp_memory_plugin_manager.md#7-quantitative-evaluation-framework-2026-industrial-standard)
- Plugin index — [./plugin_scorecard.md](plugin_scorecard.md)
- Operator manual — [../manual/mcp_memory_plugins.md](../manual/mcp_memory_plugins.md)
