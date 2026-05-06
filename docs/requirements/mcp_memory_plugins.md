# MCP Memory Plugins PRD

This PRD captures the product-level requirements for the MCP memory plugin manager
(Phase 1). It is the contract reviewers gate against; mechanics live in the design
proposal at [../proposals/mcp_memory_plugin_manager.md](../proposals/mcp_memory_plugin_manager.md).

## 1. Goal

Refactor the memory backend behind a **plugin contract** so different engines can be
plugged in without changing the public MCP tool schemas. Phase 1 ships exactly one
plugin (`rag_plugin`, behavior-preserving over the existing Postgres + pgvector + BM25
+ rerank stack) and the manager that fronts it. A `Manager` selects the active plugin
at request scope using two inputs only: an optional per-call `plugin` argument on the
tool input and a single global `default_plugin` setting. There is no per-project,
per-API-key, or per-tenant override layer.

## 2. Non-goals

- No change to the public MCP tool schemas beyond the additive optional `plugin` field.
- No change to the `memory_*` session-memory tools (`memory_before_turn`,
  `memory_after_turn`, `memory_run_maintenance`, `memory_list_dir_with_abstract`).
- No automatic migration of existing tenant data into a non-default plugin.
- v1 is pure Go, in-process. No external language runtime, no cgo, no subprocess, no
  extra container in the deploy artifact.
- Not a vector-DB replacement. `rag_plugin` remains the right answer for at-scale coarse
  retrieval across an unbounded corpus.

## 3. User stories

| ID  | Actor      | Story                                                                                                    |
| --- | ---------- | -------------------------------------------------------------------------------------------------------- |
| U1  | Agent      | Write a path without specifying a plugin and have it stored under the operator-configured default.       |
| U2  | Agent      | Read or search a path it just wrote with no extra arguments and observe the same content.                |
| U3  | Agent      | Pin a single call to a named plugin via the optional `plugin` argument and bypass the default.           |
| U4  | Agent      | Receive a clear `INVALID_ARGUMENT` with the list of valid plugin names when it passes a typo.            |
| U5  | Agent      | Receive a `NOT_FOUND` with a routing hint when it reads a path under the wrong plugin.                   |
| U6  | Operator   | Switch the global default plugin via config and restart, without changing tool schemas or call shape.    |
| U7  | Operator   | Continue running pre-refactor configs (`settings.mcp.files.*`) for one minor with a single startup WARN. |
| U8  | Operator   | Observe per-plugin call volume, latency, and (Phase 2) cost via `mcp.memory.<plugin>.*` metric prefix.   |
| U9  | Reviewer   | Read a frozen `baseline_v1` scorecard and reproduce it locally with a documented Make target.            |

## 4. Functional requirements

Each `FR-*` lists its mapped acceptance criterion from
[the proposal §6 / §6.1](../proposals/mcp_memory_plugin_manager.md#6-acceptance-criteria).

- **FR-1 — Plugin contract.** A `Plugin` interface in `internal/mcp/memory/plugin/`
  exposes `Name()`, `Capabilities()`, the seven `file_*` operations, and `Start`/`Stop`
  lifecycle hooks. Result types are shared across plugins. *Acceptance:* A4.
- **FR-2 — Plugin manager.** A `Manager` registers plugins by name, holds the configured
  default, and resolves a `Plugin` per call from `(per-call override, default)`. Auth
  and project are accepted for logging only. *Acceptance:* A3, A4.
- **FR-3 — `rag_plugin`.** A package `internal/mcp/memory/plugins/rag` wraps the existing
  `*files.Service` without behavior change. *Acceptance:* A1.
- **FR-4 — Optional `plugin` argument.** Every `file_*` tool input gains an optional
  `plugin` field (enum `["rag", "pageindex", "auto"]`, default `"auto"`). Other input
  fields are byte-identical to the prior schema. *Acceptance:* A2.
- **FR-5 — Routing rules.** Resolution order is per-call argument → `default_plugin` →
  `"rag"`. No per-project / per-API-key / per-tenant override map. *Acceptance:* A3.
- **FR-6 — Unknown plugin name.** Returns `INVALID_ARGUMENT` with
  `available_plugins=[...]` in `structuredContent`. *Acceptance:* A4.
- **FR-7 — Cross-plugin reads.** `file_read` / `file_stat` / `file_list` / `file_search`
  pinned to plugin B against a path written under plugin A return `NOT_FOUND` with a
  hint identifying plugin A. The manager never silently falls back across plugins.
  *Acceptance:* A4, A8.
- **FR-8 — Sticky mutations.** A `file_write` under plugin X stores the path under X
  only. There is no implicit migration. *Acceptance:* A4.
- **FR-9 — Resolved-plugin observability.** Every request log entry records the resolved
  plugin name. Per-plugin metrics use the prefix `mcp.memory.<plugin>.*`. *Acceptance:*
  A9, plus E04 from §5.3.
- **FR-10 — Default routing knob.** `settings.mcp.memory.default_plugin` selects the
  default plugin (Phase 1: only `"rag"` accepted at runtime). *Acceptance:* A3.
- **FR-11 — Legacy-config shim.** `settings.mcp.files.*` is translated into
  `settings.mcp.memory.plugins.rag.*` at config load time, with exactly one WARN line
  per process. The shim is removed in the next minor after Phase 3. *Acceptance:* A5.
- **FR-12 — Reverting to single-plugin mode.** Setting `default_plugin=rag` and removing
  any non-rag plugin block from config produces pre-refactor behavior on every input.
  *Acceptance:* A10.
- **FR-13 — Eval harness shipping with Phase 1.** `make eval-plugin PLUGIN=rag` and
  `make eval-baseline-rag` run the §7.3 suite end-to-end via `go run`, with no external
  language runtime. *Acceptance:* A9 (docs), Q1, Q2, Q5–Q8 (`rag_plugin` slice), Q10–Q12.
- **FR-14 — Frozen Phase-1 baseline.** `docs/eval/baseline_v1/rag_plugin_scorecard.md`,
  `raw_per_query.jsonl`, and `run_metadata.yml` land in the same PR as the Phase 1 code
  and are not overwritten without a `CHANGELOG.md` entry. *Acceptance:* A9; gates Q1–Q12.

## 5. Quality requirements

Each `Q*` mirrors the proposal §6.1 ID; the threshold column is the §7.5 hard gate
applicable to `rag_plugin` in Phase 1.

| ID   | Metric                                              | `rag_plugin` hard gate                |
| ---- | --------------------------------------------------- | ------------------------------------- |
| Q1   | Internal Recall@10 / nDCG@10 / MRR / Hit@5          | ≥ baseline_v1 − 2 pp on overall; long-doc nDCG@10 ≥ baseline_v1 |
| Q2   | RAGAS faithfulness / context_recall / answer_correctness | ≥ 0.85 / ≥ 0.80 / ≥ 0.75            |
| Q3   | FinanceBench-150 accuracy                           | ≥ 0.50 (acknowledged weak corpus)     |
| Q4   | LongMemEval_S accuracy                              | ≥ 0.55                                |
| Q5   | BEAM-1M (200 subset) overall and abstention         | ≥ 0.40 overall, ≥ 0.50 abstention     |
| Q6   | `file_search` p95 / p99 latency                     | ≤ baseline_v1 × 1.05 / × 1.10         |
| Q7   | $ per 1 000 `file_search` calls                     | ≤ $0.50                               |
| Q8   | Index throughput                                    | ≥ 1 500 text-pages/min/worker         |
| Q9   | Prompt-injection blocked (12-attack catalogue)       | ≥ 11/12                               |
| Q10  | Cross-tenant retrieval probe                        | **0** hits across 100 probes          |
| Q11  | Supersession + GDPR-delete recall                   | ≥ 48/50; p95 time-to-unreachable ≤ 1 000 ms |
| Q12  | Weekly drift on golden nDCG@10                      | ≤ 2 pp absolute                       |

`baseline_v1` is the scorecard captured at Phase-1 ship for `rag_plugin`. It is frozen
for the life of v1; a new baseline is only adopted on a deliberate scorecard reset
(rare, documented in `CHANGELOG.md`).

## 6. Out-of-scope

- `pageindex_plugin` (Phase 2). The Phase 1 binary does not ship the indexer, the
  Responses-API LLM client, the tree search loop, or the tree-reasoning prompts.
- `SystemFS`, the `mcp_files.system_owner` column, and the SQL lint gate (Phase 2).
  Phase 1 does not introduce a system namespace because no Phase 1 plugin needs one.
- Session-memory `memory_*` tools gaining a `plugin` argument (deferred per the
  proposal §2.7 — pinning a backend per session via settings is a cleaner contract).
- Anthropic / multi-provider LLM contracts (proposal §3.6). v1 commits to the OpenAI
  Responses API only; this is a Phase 2 / Phase 3 concern when relevant at all.
- Per-project / per-API-key / per-tenant override maps (rejected in proposal §2.4).

## 7. Open issues

Tracked in [../proposals/mcp_memory_plugin_manager.md#3-risks--open-questions](../proposals/mcp_memory_plugin_manager.md#3-risks--open-questions):

- §3.1 Cost (Phase 2 concern; Phase 1 has no LLM in the hot path).
- §3.2 Algorithmic drift from upstream PageIndex (Phase 2).
- §3.3 Hardening the Go implementation — PDF parser, tokenizer parity (Phase 2).
- §3.4 Rename / random-IO mismatch (Phase 2).
- §3.5 Single shared `*files.Service` is a hot dependency (Phase 1 — mitigated by
  per-plugin metrics + E05 latency gate).
- §3.6 LLM provider lock-in (Phase 2).
- §3.7 `mcp_files.system_owner` migration on a populated cluster (Phase 2).

## 8. References

- Design proposal — [../proposals/mcp_memory_plugin_manager.md](../proposals/mcp_memory_plugin_manager.md)
- Operator manual — [../manual/mcp_memory_plugins.md](../manual/mcp_memory_plugins.md)
- File-tool requirements — [mcp_files.md](mcp_files.md)
