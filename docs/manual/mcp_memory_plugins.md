# MCP Memory Plugins Operator Manual

This manual is for operators running the MCP server and routing memory traffic between
backends. It covers configuration, routing rules, capability vectors, bring-up procedures,
telemetry, and troubleshooting for the plugin manager that fronts the `file_*` toolset.

## 1. Overview

The `file_*` tools (`file_stat`, `file_read`, `file_write`, `file_delete`, `file_rename`,
`file_list`, `file_search`) are served through a plugin manager. The manager owns one or
more registered backends, each implementing a uniform `Plugin` interface, and resolves a
plugin per call from two inputs: an optional `plugin` field on the tool input and a single
global `default_plugin` setting. The public tool schemas are unchanged apart from that one
additive optional field.

Plugins shipped today (Phase 1):

| Plugin | Status   | Engine                                                      |
| ------ | -------- | ----------------------------------------------------------- |
| `rag`  | Shipping | Existing Postgres + pgvector + BM25 + optional rerank stack |

`pageindex` is not shipped in Phase 1. See [Section 6](#6-pageindex-phase-2).

Background and the full design rationale live in
[../proposals/mcp_memory_plugin_manager.md](../proposals/mcp_memory_plugin_manager.md).

## 2. Configuration

The plugin manager reads `settings.mcp.memory.*`.

```yaml
settings:
  mcp:
    memory:
      default_plugin: "rag"            # "rag" today; "pageindex" reserved (Phase 2)
      plugins:
        rag: {}                         # all current settings.mcp.files.* re-rooted under here
        # pageindex: see Section 6 — block is reserved, not used in Phase 1
```

`settings.mcp.memory.plugins.rag.*` carries the keys that previously lived under
`settings.mcp.files.*` (chunking, embedder, BM25, rerank, async indexing, version
retention). Field semantics are unchanged; only the YAML location moved.

### 2.1 Legacy configuration shim

Operators running pre-refactor configs can keep using `settings.mcp.files.*`. At config
load time the server translates any legacy keys into `settings.mcp.memory.plugins.rag.*`
and emits exactly **one** WARN line per process announcing the deprecation. The shim is
**removed in the next minor release after Phase 3**. Migrate at your earliest convenience
by moving the keys to their new location; equivalent settings produce identical observable
behavior (acceptance A5 in the proposal).

If you set both the legacy and the new key for the same logical setting, the new key
wins. The shim only fills holes; it never overrides.

## 3. Routing rules

Resolution order (first match wins):

1. **Per-call argument** — every memory-surface tool gains an optional `plugin` field.
2. **Default** — `settings.mcp.memory.default_plugin`, fallback `"rag"`.

There is no per-project, per-API-key, or per-tenant override map. If a class of project
needs a non-default plugin, teach the agent to pass `plugin=<name>` for those projects.
If everyone needs to move, flip `default_plugin`.

### 3.1 The `plugin` argument

```json
{
  "plugin": {
    "type": "string",
    "enum": ["rag", "pageindex", "auto"],
    "default": "auto",
    "description": "Memory backend to handle this call. 'auto' uses settings; explicit values pin this single call."
  }
}
```

| Value         | Meaning                                                                                                                                 |
| ------------- | --------------------------------------------------------------------------------------------------------------------------------------- |
| omitted       | Identical to `"auto"`.                                                                                                                  |
| `"auto"`      | Resolve via `default_plugin`.                                                                                                           |
| `"rag"`       | Pin this single invocation to `rag_plugin`.                                                                                             |
| `"pageindex"` | Pin to `pageindex_plugin` (Phase 2; returns `FAILED_PRECONDITION` today if not enabled).                                                |
| unknown name  | `INVALID_ARGUMENT`; response includes `available_plugins=[...]` in `structuredContent` so the caller can self-correct.                  |

### 3.2 Cross-plugin reads

A path written under plugin A is **not** silently visible to a read pinned to plugin B.
If a `file_read` / `file_stat` / `file_list` / `file_search` is pinned to plugin B and
the path does not exist there, the response is `NOT_FOUND` with a hint of the form
`"path exists under plugin=A; pass plugin=A to read it"`. The manager never falls back
across plugins; doing so would defeat tenant isolation between engines and surprise the
caller.

Mutations are sticky: a `file_write` under plugin X stores the path under X only. There
is no implicit migration on write or read.

## 4. Capabilities

Each plugin advertises a capability vector consumed by error hints and operator dashboards.
The schema is unchanged across plugins — only the values differ.

| Field              | `rag` value                                                              |
| ------------------ | ------------------------------------------------------------------------ |
| `SearchModes`      | `["hybrid", "semantic", "lexical"]`                                      |
| `SupportsRandomIO` | `true`                                                                   |
| `SupportsRename`   | `true`                                                                   |
| `SupportsVersions` | `true` (rows in `mcp_file_versions`)                                     |
| `MaxPayloadBytes`  | inherited from the existing `mcp_files` payload limits                   |
| `AsyncIndexing`    | `true`                                                                   |
| `FreshnessWindow`  | `5s` (search visibility lag after a successful `file_write`)             |
| `Notes`            | "Hybrid pgvector + BM25 + optional rerank"                                |

The freshness window is part of the user contract documented for the conformance suite
row C05/R06 in [the proposal §5.1 / §5.2](../proposals/mcp_memory_plugin_manager.md#51-conformance-suite--user-observable-scenarios).

## 5. Operating procedures

### 5.1 Bring-up checklist

1. Confirm `settings.mcp.memory.default_plugin` is set (defaults to `"rag"` if absent).
2. Confirm legacy `settings.mcp.files.*` keys are either migrated or relied on intentionally
   (a single startup WARN means the shim is active).
3. Restart the MCP server. The manager constructs `rag_plugin` and starts it; failure to
   build any registered plugin aborts startup with a wrapped error naming the plugin.
4. Hit `tools/list` from a known-good MCP client. Every `file_*` tool must show one new
   optional `plugin` field; everything else byte-identical to the prior schema.
5. Run a write/list/stat/search/read/delete script under the default plugin; expect the
   pre-refactor behavior unchanged (acceptance A1).

### 5.2 Switching the default

1. Update `settings.mcp.memory.default_plugin` to the new plugin name. Today only `"rag"`
   is valid.
2. Restart the server. The change is config-reload, no migration required.
3. Replay the smoke script. Existing data written under the prior default remains pinned
   to that plugin and is reachable only with an explicit `plugin=<prior>` argument.

### 5.3 Verifying routing

Every tool invocation logs the resolved plugin name on its request log entry. Per-plugin
call volumes are observable via the metric prefix `mcp.memory.<plugin>.*`
([Section 8](#8-telemetry)).

The reference acceptance set for "working" lives at
[../proposals/mcp_memory_plugin_manager.md#6-acceptance-criteria](../proposals/mcp_memory_plugin_manager.md#6-acceptance-criteria).
A1, A2, A3, A5, A9 cover Phase 1; the remainder track Phases 2–4.

## 6. Pageindex (Phase 2)

`pageindex_plugin` is **not shipped in Phase 1.** The Phase 1 binary does not contain the
indexer, the Responses-API LLM client, the tree search loop, or the `SystemFS`
persistence layer. Explicit `plugin="pageindex"` calls today return
`FAILED_PRECONDITION` with a message naming the missing plugin (acceptance A10).

When Phase 2 ships, enabling it will require a config block of the shape below; the
keys are reserved and operators may stage the values in a separate file ahead of the
release. **Do not uncomment or paste this block into a Phase 1 deploy** — it has no
effect today and the validator may evolve before Phase 2.

```yaml
# RESERVED FOR PHASE 2 — has no effect in Phase 1.
settings:
  mcp:
    memory:
      plugins:
        pageindex:
          llm:
            api_key: ""        # required at Phase 2; empty key disables the plugin
            base_url: ""       # empty = api.openai.com
            indexing_model: "gpt-5.4-mini"
            retrieve_model: "gpt-5.4-mini"
          # indexer / algo / tree_query / pdf knobs ship with Phase 2.
```

The full reserved schema and the resolution semantics of an empty `llm.api_key` (silently
unregistered vs. fail-fast on present-but-empty) are specified in
[../proposals/mcp_memory_plugin_manager.md#24-settings--resolution-rules](../proposals/mcp_memory_plugin_manager.md#24-settings--resolution-rules).

## 7. Eval harness

Plugin quality is tracked by a pure-Go scorecard harness under
`internal/mcp/memory/conformance/eval/`. Operators rarely run it locally; CI runs it
nightly per plugin and on every PR that touches plugin code. The runbook lives at
[../eval/README.md](../eval/README.md).

Two top-level Make targets:

```bash
# Run the §7.3 suite for a plugin against the per-PR run directory.
make eval-plugin PLUGIN=rag

# Re-capture the frozen Phase-1 baseline. Reviewers only.
make eval-baseline-rag
```

The frozen Phase-1 baseline lives at
[../eval/baseline_v1/rag_plugin_scorecard.md](../eval/baseline_v1/rag_plugin_scorecard.md).
It is committed once and **never overwritten** without a `CHANGELOG.md` entry and a
documented baseline reset (proposal §7.5).

The plugin index page at [../eval/plugin_scorecard.md](../eval/plugin_scorecard.md) lists
every plugin's latest scorecard.

## 8. Telemetry

### 8.1 Metric prefix

Per-plugin metrics carry the prefix `mcp.memory.<plugin>.*`. The exact label set is owned
by the metric registry, but the prefix is stable. Use it to attribute load and latency
to the right plugin in dashboards.

### 8.2 Per-call billing metadata

Tool-result `structuredContent` carries per-call billing fields used for cost attribution
(proposal acceptance E04 and A7):

| Field             | Plugin       | Notes                                                                         |
| ----------------- | ------------ | ----------------------------------------------------------------------------- |
| `tokens_in`       | `pageindex`  | LLM input tokens consumed by `file_search` (Phase 2).                         |
| `tokens_out`      | `pageindex`  | LLM output tokens (Phase 2).                                                  |
| `llm_calls`       | `pageindex`  | Number of LLM round-trips (Phase 2).                                          |
| `truncated`       | `pageindex`  | `true` when a per-call budget capped the response (Phase 2).                  |

`rag_plugin` does not emit `tokens_*` or `llm_calls` because LLM calls are not in its
hot path; embedding cost is reported through the existing embedder telemetry and is
attributed to the user's own credentials.

The resolved plugin name is recorded on every request log entry regardless of plugin.

## 9. Troubleshooting

| Symptom                                                                                  | Likely cause                                                                                            | Action                                                                                       |
| ---------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------- |
| Tool returns `INVALID_ARGUMENT` with `available_plugins=[...]`                            | Caller passed an unknown plugin name (typo, version mismatch, future plugin not yet shipped).            | Pass one of the listed names, or omit the field to use `default_plugin`.                     |
| Tool returns `NOT_FOUND` with hint "path exists under plugin=A; pass plugin=A to read it" | Path was written via plugin A and the current call is pinned to plugin B (or vice versa).                | Re-issue the call with `plugin=A`. The manager does not silently cross plugins by design.    |
| Tool returns `FAILED_PRECONDITION` for `plugin="pageindex"`                                | `pageindex_plugin` is not enabled (Phase 1; or Phase 2 with empty `llm.api_key`).                        | Either drop the `plugin` field (route via default) or wait for Phase 2 / configure the key.  |
| Single startup WARN about deprecated `settings.mcp.files.*`                               | Legacy-config shim translated old keys.                                                                  | Move keys to `settings.mcp.memory.plugins.rag.*` before the shim is removed (post-Phase 3).  |
| Two routing layers seem active                                                            | There is no second layer. The only inputs are the per-call `plugin` argument and `default_plugin`.       | Confirm by reading the request log entry's resolved-plugin field.                            |

## 10. References

- Design proposal — [../proposals/mcp_memory_plugin_manager.md](../proposals/mcp_memory_plugin_manager.md)
- Architecture overview — [../arch/mcp_memory.md](../arch/mcp_memory.md)
- File-tool manual — [mcp_files.md](mcp_files.md)
- Memory tool manual — [mcp_memory.md](mcp_memory.md)
- Eval runbook — [../eval/README.md](../eval/README.md)
- Plugin requirements — [../requirements/mcp_memory_plugins.md](../requirements/mcp_memory_plugins.md)
