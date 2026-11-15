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

Plugins shipped today:

| Plugin      | Status                    | Engine                                                                           |
| ----------- | ------------------------- | -------------------------------------------------------------------------------- |
| `rag`       | Shipping (Phase 1)        | Existing Postgres + pgvector + BM25 + optional rerank stack                      |
| `pageindex` | Shipping (Phase 2; gated) | Tree-reasoning indexer + Responses-API LLM + bbolt cache; gated on `llm.api_key` |

See [Section 6](#6-pageindex-phase-2) for pageindex bring-up.

Background and the full design rationale live in
[../proposals/mcp_memory_plugin_manager.md](../proposals/mcp_memory_plugin_manager.md).

## 2. Configuration

The plugin manager reads `settings.mcp.tools.memory.*`.

```yaml
settings:
  mcp:
    memory:
      default_plugin: 'rag' # "rag" today; "pageindex" reserved (Phase 2)
      plugins:
        rag: {} # all current settings.mcp.files.* re-rooted under here
        # pageindex: see Section 6 — block is reserved, not used in Phase 1
```

`settings.mcp.tools.memory.plugins.rag.*` carries the keys that previously lived under
`settings.mcp.files.*` (chunking, embedder, BM25, rerank, async indexing, version
retention). Field semantics are unchanged; only the YAML location moved.

### 2.1 Legacy configuration shim

Operators running pre-refactor configs can keep using `settings.mcp.files.*`. At config
load time the server translates any legacy keys into `settings.mcp.tools.memory.plugins.rag.*`
and emits exactly **one** WARN line per process announcing the deprecation. The shim is
**removed in the next minor release after Phase 3**. Migrate at your earliest convenience
by moving the keys to their new location; equivalent settings produce identical observable
behavior (acceptance A5 in the proposal).

If you set both the legacy and the new key for the same logical setting, the new key
wins. The shim only fills holes; it never overrides.

## 3. Routing rules

Resolution order (first match wins):

1. **Per-call argument** — every memory-surface tool gains an optional `plugin` field.
2. **Default** — `settings.mcp.tools.memory.default_plugin`, fallback `"rag"`.

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

| Value         | Meaning                                                                                                                |
| ------------- | ---------------------------------------------------------------------------------------------------------------------- |
| omitted       | Identical to `"auto"`.                                                                                                 |
| `"auto"`      | Resolve via `default_plugin`.                                                                                          |
| `"rag"`       | Pin this single invocation to `rag_plugin`.                                                                            |
| `"pageindex"` | Pin to `pageindex_plugin` (Phase 2; returns `FAILED_PRECONDITION` today if not enabled).                               |
| unknown name  | `INVALID_ARGUMENT`; response includes `available_plugins=[...]` in `structuredContent` so the caller can self-correct. |

### 3.2 Cross-plugin reads

A path written under plugin A is **not** silently visible to a read pinned to plugin B.
If a `file_read` / `file_stat` / `file_list` / `file_search` is pinned to plugin B and
the path does not exist there, the response is `NOT_FOUND` with a hint of the form
`"path exists under plugin=A; pass plugin=A to read it"`. The manager never falls back
across plugins; doing so would defeat tenant isolation between engines and surprise the
caller.

Mutations are sticky: a `file_write` under plugin X stores the path under X only. There
is no implicit migration on write or read.

### 3.3 File-level summaries in search results

Regardless of the resolved plugin, every `file_search` hit returns a `file_summary`: a
concise, English, file-level overview (≤ 300 words / 2,048 bytes) bound to the same
content generation as the matched chunk. `rag` publishes it through its asynchronous
index worker (a just-written file may briefly return without one; a slow or missing
summarizer yields a bounded deterministic fallback, never an empty field). `pageindex`
returns its bounded `Tree.DocDescription`. The summary is display-only response
metadata and never changes ranking, and it is redacted from logs and call audits. See
[`docs/proposals/file_search_file_summaries.md`](../proposals/file_search_file_summaries.md)
for configuration (`...plugins.rag.index.file_summary.*`), readiness, and rollback.

## 4. Capabilities

Each plugin advertises a capability vector consumed by error hints and operator dashboards.
The schema is unchanged across plugins — only the values differ.

| Field              | `rag` value                                                  |
| ------------------ | ------------------------------------------------------------ |
| `SearchModes`      | `["hybrid", "semantic", "lexical"]`                          |
| `SupportsRandomIO` | `true`                                                       |
| `SupportsRename`   | `true`                                                       |
| `SupportsVersions` | `true` (rows in `mcp_file_versions`)                         |
| `MaxPayloadBytes`  | inherited from the existing `mcp_files` payload limits       |
| `AsyncIndexing`    | `true`                                                       |
| `FreshnessWindow`  | `5s` (search visibility lag after a successful `file_write`) |
| `Notes`            | "Hybrid pgvector + BM25 + optional rerank"                   |

The freshness window is part of the user contract documented for the conformance suite
row C05/R06 in [the proposal §5.1 / §5.2](../proposals/mcp_memory_plugin_manager.md#51-conformance-suite--user-observable-scenarios).

## 5. Operating procedures

### 5.1 Bring-up checklist

1. Confirm `settings.mcp.tools.memory.default_plugin` is set (defaults to `"rag"` if absent).
2. Confirm legacy `settings.mcp.files.*` keys are either migrated or relied on intentionally
   (a single startup WARN means the shim is active).
3. Restart the MCP server. The manager constructs `rag_plugin` and starts it; failure to
   build any registered plugin aborts startup with a wrapped error naming the plugin.
4. Hit `tools/list` from a known-good MCP client. Every `file_*` tool must show one new
   optional `plugin` field; everything else byte-identical to the prior schema.
5. Run a write/list/stat/search/read/delete script under the default plugin; expect the
   pre-refactor behavior unchanged (acceptance A1).

### 5.2 Switching the default

1. Update `settings.mcp.tools.memory.default_plugin` to the new plugin name. Today only `"rag"`
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

`pageindex_plugin` ships in Phase 2. The runtime (indexer, Responses-API LLM client,
tree search loop, bbolt response cache, and `SystemFS`-backed persistence) links into
the existing binary; there is no new process or container. The plugin is gated on
`llm.api_key`: the manager constructs and registers it only when the key is non-empty.
With an empty key the plugin is silently skipped and a single debug log line is
emitted at startup; explicit `plugin="pageindex"` calls then return `INVALID_ARGUMENT`
with `available_plugins=[...]` and a hint that names the missing
`settings.mcp.tools.memory.plugins.pageindex.llm.api_key` key (acceptance A10).

### 6.1 Enabling pageindex

Set `settings.mcp.tools.memory.plugins.pageindex.*`. Defaults are sourced from
[../../internal/mcp/memory/plugins/pageindex/settings.go](../../internal/mcp/memory/plugins/pageindex/settings.go).
With `default_plugin: rag` and an empty `pageindex.llm.api_key`, the manager registers
only `rag_plugin`; explicit `plugin="pageindex"` calls then surface the missing-key
hint described in [Section 9](#9-troubleshooting).

```yaml
settings:
  mcp:
    tools:
      memory:
        default_plugin: 'rag'
        plugins:
          rag: {}
          pageindex:
            llm:
              api_key: '' # empty key keeps pageindex unregistered
              base_url: '' # empty = api.openai.com; an origin selects its /v1 API root
              indexing_model: 'gpt-5.4-mini'
              retrieve_model: 'gpt-5.4-mini'
            indexer:
              timeout_index: '5m'
              timeout_query: '60s'
              max_concurrency: 8
              retry:
                max_attempts: 3 # upstream requests per call, including the first
                initial_backoff: '250ms'
                max_backoff: '8s'
              cache:
                enabled: true
                path: '/var/lib/laisky/pageindex-cache.bbolt'
                max_size_bytes: 1073741824
            algo:
              toc_check_page_num: 20
              max_page_num_each_node: 10
              max_token_num_each_node: 20000
              generate_node_summary: true
              generate_doc_description: true
            tree_query:
              max_steps: 8
              max_tokens: 20000
              candidate_docs: 5
            pdf:
              text_parser: 'pdfcpu'
              outline_parser: 'pdfcpu'
            limits: # absolute ceilings; 0 or a negative value selects the default
              max_nodes_per_document: 2000
              max_llm_calls_per_document: 256
              max_tokens_per_document: 2000000
              window: '1h'
              tenant_llm_calls_per_window: 1000
              tenant_tokens_per_window: 4000000
              global_llm_calls_per_window: 5000
              global_tokens_per_window: 20000000
              global_reserve_percent: 20
              max_tracked_tenants: 10000
              max_output_tokens_per_call: 4096
              max_toc_output_tokens_per_call: 32768
```

The full schema and the resolution semantics of an empty `llm.api_key` (silently
unregistered vs. fail-fast on present-but-empty) are specified in
[../proposals/mcp_memory_plugin_manager.md#24-settings--resolution-rules](../proposals/mcp_memory_plugin_manager.md#24-settings--resolution-rules).

### 6.2 Bring-up checklist

1. Provision the bbolt cache parent directory (default `/var/lib/laisky/`); the
   process user must have write permission. The plugin opens the file lazily on the
   first indexing call and never deletes it.
2. Set `llm.api_key`. Restart. The startup log names `pageindex` in the registered
   plugin list.
3. To route a single call through pageindex, pass `plugin="pageindex"` on any `file_*`
   tool. To route by default, set `default_plugin: "pageindex"`.

### 6.3 System namespace

Pageindex persists tree JSON (one row per indexed document) in `mcp_files` rows owned
by the system namespace under `system_owner='pageindex'`. These rows are unobservable
to user-facing tool calls regardless of API key (acceptance A8); the namespace is
read/written only through the `SystemFS` handle the plugin obtains at startup. The
`WriteOpts.SkipRAGIndex` write option suppresses pgvector / BM25 indexing for these
system rows.

### 6.4 Per-call billing metadata

Tool-result `structuredContent` for `pageindex`-routed `file_search` carries
`tokens_in`, `tokens_out`, `llm_calls`, and `truncated` per [Section 8.2](#82-per-call-billing-metadata).
Phase 2 indexing is **synchronous**: a `file_write` of a fresh document blocks the
caller until the tree is built, for at most `indexer.timeout_index`. When that time
runs out during LLM work, the remaining LLM phases are skipped like an exhausted
budget (§6.6): leaves get local summaries, a PDF keeps the structure found so far,
and the tree is stored. If it runs out before the structure is extracted, the write
still succeeds with a deterministic file summary and the previous tree (if any)
stays inactive until the next write. Search
loops the tree-reasoning prompt over the configured `tree_query.candidate_docs` and
issues at most one planner call per candidate and `tree_query.max_steps` in total.
`tree_query.max_tokens` is enforced before each call is sent, for the planner's
input and its answer together: the planner first reserves a quarter of the
remaining budget for its answer (at least 256 tokens, at most
`limits.max_output_tokens_per_call`), and its prompt uses the most detailed outline
that fits the rest (full outline, then without summaries, then at most three, two
or one levels, then fewer top-level sections). Search calls also draw from the
caller's rolling budget (§6.6).

When the planner cannot run for a candidate (a budget refused it, not even one
section fits `tree_query.max_tokens`, its answer was cut or incomplete, or the
provider failed), search falls back without an LLM call. It returns the sections
whose title or summary shares the most query terms (a title term counts twice), at
most three that do not overlap, preferring the narrower section and then document
order; a section's pages include those of the sections nested under it. Terms are
case-folded words of at least two characters without common English stop words,
and character pairs of Chinese, Japanese and Korean text; at most 32 distinct query
terms and 4 KiB of each title and summary are compared. Only when no section
matches is the first section returned. Fallback results are ranked below the
sections the planner chose for other candidates (their scores are halved, or
quartered for the first-section default), so a guess never displaces a planned
result. The search result itself is unchanged; the plugin counts planned
candidates and fallbacks by cause (`budget_exhausted`, `search_limit`,
`planner_failed`), logs each fallback at debug level as `pageindex.search.fallback`,
and at most once per minute logs `pageindex.search.fallbacks` at warn level with the
planned, matched and first-section counts since the previous summary.

Page ranges, whether from the planner or the fallback, are resolved against the
tree's own pages, so a range is never more expensive than the pages it names, however
wide the planner makes it.

### 6.5 Watch items

Per the wave-B implementation:

- **PDF text extraction.** `pdfcpu` v0.12.0's pure-Go API does not expose plain-text
  extraction; pageindex routes text through `dslipak/pdf` for both parser modes
  while keeping `pdfcpu` for outline parsing. Operators with text-quality regressions
  on multi-column or heavily-figured PDFs should expect a corpus-comparison row in
  the §7 scorecard once datasets land.
- **Recursive node expansion.** Proposal §2.6.4.1 phase 8 (recursive subtree expansion
  for nodes that exceed `algo.max_token_num_each_node`) is wired, bounded to depth 3,
  and stops when the document's call, token or node ceiling (§6.6) is reached; the
  node then stays a leaf covering its whole page range.
- **Verification + fixer prompts.** Phase 7 (title-appearance sampling of at most 10
  nodes and up to three fixer rounds) is wired. When the document budget is exhausted
  the remaining samples count as correct and the tree is kept as emitted.

### 6.6 Structural and spending limits

Indexing and search spend the operator's LLM credential, and the document shape is
chosen by the caller. Every ceiling below is therefore an absolute service-side
value under `settings.mcp.tools.memory.plugins.pageindex.limits.*`; none is derived
from the number of headings, pages, roots or table-of-contents entries. A zero or
negative value selects the default, so a ceiling can be raised or lowered but not
switched off. The defaults leave typical documents unchanged.

| Key | Default | Applies to |
| --- | --- | --- |
| `max_nodes_per_document` | `2000` | Tree nodes of one document. Markdown headings past the cap stay in the preceding section's text, so search still reaches them; PDF table-of-contents entries past the cap are dropped and an expansion that would exceed it is skipped. Only nodes that are inserted count: table-of-contents entries without a title are dropped first, and an expansion whose first generated section repeats the parent's title promotes the sections nested under it and counts only those. |
| `max_llm_calls_per_document` | `256` | Upstream calls of one indexing run, across every phase (table-of-contents detection and parsing, summaries, verification, expansion, description). |
| `max_tokens_per_document` | `2000000` | Tokens of one indexing run. The earlier per-node allowance (`algo.max_token_num_each_node` × (roots + 1) for Markdown, × pages + `tree_query.max_tokens` for PDF) still applies when it is smaller. |
| `window` | `1h` | Length of the sliding window of the rolling budgets below. |
| `tenant_llm_calls_per_window` | `1000` | Upstream calls of one caller (API-key hash) per window, indexing and search combined. |
| `tenant_tokens_per_window` | `4000000` | Tokens of one caller per window. |
| `global_llm_calls_per_window` | `5000` | Upstream calls of the whole process per window. This is the operator's ceiling even when callers use many API keys. |
| `global_tokens_per_window` | `20000000` | Tokens of the whole process per window. |
| `global_reserve_percent` | `20` | Share of the global budget only light callers may spend (below). Capped at 90. |
| `max_tracked_tenants` | `10000` | Callers whose rolling budget is held in memory. |
| `max_output_tokens_per_call` | `4096` | Tokens one upstream request with a short answer (a summary, a verdict, page ranges) may generate, reasoning tokens included. Raised to 256 when set lower. |
| `max_toc_output_tokens_per_call` | `32768` | Tokens one request whose answer re-emits a table of contents (transformation, page-number extraction, generation) may generate. Never below `max_output_tokens_per_call`. |

How the ceilings are enforced:

- Every upstream request, for indexing and for search, passes one admission point.
  Before sending, it reserves the request's whole possible cost: the estimated input
  tokens (prompt, message framing and any output schema) plus an output allowance,
  which is sent as the Responses API `max_output_tokens`. The allowance is the
  request kind's ceiling (`max_output_tokens_per_call` or
  `max_toc_output_tokens_per_call`), lowered to what the operation has left after its
  input. A request is refused when it cannot be granted at least 256 output tokens, or
  when the caller's rolling budget or the global budget cannot cover the reservation.
  Calls wait for a concurrency slot before reserving, so queued calls hold no budget.
  A retry of a failed request is admitted the same way before it is sent, after the
  failed request has been charged, so `max_llm_calls_per_document` and the rolling
  call budgets count upstream requests, not logical calls.
- Each upstream request is then charged, at the time it finishes:
  - the provider-reported total when the provider reports usage;
  - for an answer without usage, the input estimate plus the answer's estimated tokens;
  - for an error response from the provider, the input estimate;
  - nothing for a request the client-side rate limiter kept in the process (the
    upstream call itself stays counted);
  - for a request the provider never answered (a timeout, a cancellation or a
    connection failure), its whole reservation, because the provider may have
    processed and billed it in full.

  A charge below the reservation frees the difference at once; a charge above it is
  debt that counts against the rolling budgets for a whole `window` from the time it
  is known, so the time a slow call spent in flight cannot shorten it.
- An answer the provider cut at its output ceiling (marked incomplete for
  `max_output_tokens`, or using the whole ceiling, compared with the lower ceiling a
  server echoes when it clamped the request) is charged and counted but never used
  or cached; the work it was for degrades like a refused call (below). So is an
  answer the provider did not complete for another reason: any status other than
  `completed`, such as `incomplete` for `content_filter` or without a reason, or
  `failed`. An answer without a status is taken as complete. Note that
  the ceiling counts reasoning tokens: `gpt-5.4-mini` defaults to reasoning effort
  `none`, but a model whose default effort is higher needs a larger
  `max_output_tokens_per_call`, or more of its answers will be cut.
- Cached responses are free. The cache is consulted before any budget, so a cached
  answer is reused even when every budget is spent.
- Retries follow `indexer.retry.*` (default 3 requests per call, exponential backoff
  from 250 ms to 8 s). Only timeouts, connection failures, 408, 409, 429 and 5xx are
  retried; any other provider response (an invalid request, authentication, an
  unknown model) fails at once.
- Fairness: the last `global_reserve_percent` of the global budget is reserved for
  light callers, those that have spent less than one document's allowance
  (`max_llm_calls_per_document` calls and `max_tokens_per_document` tokens) of their
  own budget in the current window. Heavy callers are refused at the reserve, so one
  heavy caller cannot starve another caller's first document. Each caller can draw at
  most one document's allowance from the reserve, so a holder of k keys can still
  spend at most k such allowances of it; the absolute global ceiling always holds.
- A refused summary is replaced by a deterministic local summary: the first words of
  the section (at most 320 bytes), with Markdown markers removed. A refused
  table-of-contents call keeps what was found so far, or one `Document` node for the
  whole PDF. A write therefore never fails because a budget was exhausted; it gets a
  less detailed tree. A refused planner call in search falls back to the sections
  matching the query (§6.4). Reaching `indexer.timeout_index` during LLM work degrades the same way; only
  an explicit cancellation of the request aborts the run.
- A degraded tree is marked, never presented as complete: the tree and its catalog
  entry carry `degraded`, and the file's summary is published with status
  `degraded` and one of these error codes:
  - `timeout`: `indexer.timeout_index` ended LLM work;
  - `budget_exhausted`: a rolling budget refused calls;
  - `document_limit`: only the document's own ceilings applied. They refused calls,
    cut an answer at its output ceiling, or cut the structure short at
    `max_nodes_per_document`, even when no call was refused.

  The codes are listed in order of precedence. A refreshable cause is reported over
  `document_limit`, so a refresh can still recover the refused work; a refreshed
  tree that is still truncated then reports `document_limit`. The next write
  re-indexes the file.
- A `budget_exhausted` or `timeout` tree is also refreshed later. A search that meets
  it once its refresh time has passed, while the caller's budget allows, queues a
  re-index: one worker, at most 32 queued, one job per content generation.
  Refreshes wait 5 minutes, then double up to 6 hours, and stop after 5 attempts.
  A `document_limit` tree is final for its content, because a refresh would hit the
  same ceiling.
- Refresh is not guaranteed background recovery. A degraded document that is never
  searched or written again stays degraded. The refresh queue lives in memory and is
  lost on restart, but the retry state (attempts, next time) is persisted in the
  catalog, so the next search after a restart queues the refresh again.
- Rolling budgets are strict sliding windows: in any interval of `window`'s length
  they admit at most their call and token ceilings. Each budget records what it
  admitted in slots of 1/60 of `window` (one minute by default), and a request is
  admitted only when everything recorded in the last 61 slots plus its reservation
  fits, so a reservation counts for between one window and one window plus one slot.
  After a burst spends a whole budget, that budget refuses until the burst leaves the
  window; it does not refill while it is spent, as a token bucket would (a token
  bucket of the same size admits up to twice its capacity within one window). A
  caller's budget that holds nothing and has no request in flight is dropped from
  memory, so retained state is bounded by recent spenders (and its slots by the calls
  the global budget admits); when `max_tracked_tenants` callers are active, a new
  caller is refused (and degrades as above) instead of receiving an untracked budget.
  Budgets are per process and reset on restart.
- Every refusal is counted, including the cheap checks that skip rendering a prompt,
  and at most once per minute the process logs `pageindex.spend.refusals` at warn
  level with the admitted calls and the tenant, global, reserve and untracked
  refusals since the previous summary.

What the ceilings guarantee, and what they do not:

- Provider-reported spend stays within each ceiling, as long as two things hold.
  First, the provider honors `max_output_tokens`. Second, its input count agrees
  with the local tokenizer estimate, which already includes message framing and the
  schema. Any difference is settled as debt that counts for a whole window. An
  upstream that generated more than `max_output_tokens` is reported, at most once a
  minute, as `pageindex.llm.output_ceiling_ignored` at warn level.
- The provider contract is pinned by `TestResponsesProviderContract` against the
  answer shapes of OpenAI and of OpenAI-compatible gateways (Laisky/one-api, new-api,
  LiteLLM, vLLM), and checked against a real endpoint by
  `TestLiveResponsesProviderContract` when `PAGEINDEX_CONTRACT_BASE_URL` and
  `PAGEINDEX_CONTRACT_API_KEY` are set (§9). `llm.base_url` may name the gateway's
  origin, as the other OpenAI-compatible settings do: an origin selects its `/v1` API
  root, and a base URL with a path of its own is used as given.
- Budgets are per process. With several replicas, the operator's hourly ceiling is
  the number of replicas times the `global_*` values.
- PDF page text is extracted before any LLM budget applies, and its CPU and memory
  are not bounded by these settings. A small compressed PDF can expand into a large
  page text. The ceilings still bound the LLM spend that text can cause.

Markdown parsing also has fixed bounds that are not configurable. goldmark's
`Parse` takes no context and cannot be interrupted, so its work is bounded before it
starts: one linear pre-scan classifies every line (fenced and indented code, HTML
blocks, thematic breaks, headings, list items, block quotes, paragraphs; indentation
in columns with tabs to the next multiple of 4) and predicts goldmark's work. A
document is parsed by goldmark only when no line opening a container has an
estimated nesting above 32 (markers plus half the indentation columns), the summed
nesting of all lines stays under 400,000, it has at most 40,000 blocks and 80,000
content lines (non-blank lines, and blank lines inside an open list), and its
link-reference definitions need at most 2^24 line copies. Ordinary documents of the
2 MB write limit stay well below these bounds; a document over any of them is scanned
by a linear fallback that recognizes ATX and setext headings outside code and HTML
blocks and renders each title with goldmark. It resolves the document's retained
link-reference definitions (at most 512 and 64 KiB), which are parsed once per
document, so rendering does not grow with the number of definitions. On every
document in the repository it gives the same headings as goldmark. Inline markup is parsed only inside headings of at most 1 KiB, and for at
most 256 KiB of heading source and 32 Ki markup characters per document (later and
longer headings keep their raw text); titles are capped at 1 KiB. Line numbers come
from one newline index. Token counting splits runs longer than 256 bytes.

What can be interrupted: the pre-scan, the fallback scanner, the walk over goldmark's
tree, tree building, every LLM call and summarization stop promptly when the request
is canceled or `indexer.timeout_index` passes. A goldmark parse in progress cannot
be interrupted; the pre-scan keeps it to at most tens of milliseconds and tens of
megabytes for a 2 MB document. Measurements and sources are in
[../ref/pageindex_structural_budgets_20261108.md](../ref/pageindex_structural_budgets_20261108.md).

Operator guidance: raise `max_llm_calls_per_document` only together with
`indexer.timeout_index`, because summaries run one at a time; lower the global
values to cap spend per hour. An indexing run that hit a ceiling or the deadline logs
`pageindex.index.limited` at debug level with its `budget_refusals`,
`rolling_refusals`, `local_summaries`, `structure_truncated` and `deadline_reached`
values; degraded files can be found by their summary status `degraded` and error code.

## 6.7 Phase 3: Shadow replay (deferred)

The shadow-replay scaffolding exists for offline evaluation and future promotion work,
but it is not part of the runtime routing contract. Runtime selection remains simple:
the caller may pass the optional `plugin` field on each `file_*` request, otherwise the
server uses `settings.mcp.tools.memory.default_plugin` and defaults to `rag`.

The wrapper lives at [../../internal/mcp/memory/plugin/shadow.go](../../internal/mcp/memory/plugin/shadow.go).
Operating semantics:

- **Live first, shadow fire-and-forget.** Mutations (`file_write`, `file_delete`,
  `file_rename`) call live first; on success the same operation is dispatched to
  shadow on a bounded goroutine pool. The user only ever sees live's response. A
  shadow failure is logged but never alters or delays the live path.
- **Search dual-read.** Both plugins answer; the live result is returned, the shadow
  result is captured for offline scoring.
- **Recorder output.** Each pair appends one JSONL record to the operator-chosen
  recorder path; no fixed runtime config path is required because the server does not
  auto-enable shadow replay.
- **Promotion gate.** Per proposal §7.8, the analyzer at
  [../../cmd/promote-pageindex/main.go](../../cmd/promote-pageindex/main.go) computes
  the win-rate over a captured period:

  | Win-rate            | Decision                                                       |
  | ------------------- | -------------------------------------------------------------- |
  | ≥ 55% with p < 0.05 | Promote: flip `default_plugin` to `pageindex` for the project. |
  | 45–55%              | Stay on `rag_plugin` (no statistically significant gain).      |
  | < 45%               | File a bug; pageindex regression.                              |

  The statistical test is a paired two-sided permutation test, B = 10 000 shuffles,
  matching §7.6.

### 6.6.1 Real-judge mode

The driver ships a `--judge=stub` mode for wiring smoke tests and two real
modes that call the OpenAI Responses API. Secrets are read from the
environment only — `OPENAI_API_KEY` is required; there is no `--api-key` flag.

```bash
OPENAI_API_KEY=sk-... go run ./cmd/promote-pageindex \
    --records=/var/log/mcp/shadow.jsonl \
    --judge=ensemble \
    --judge-model=gpt-5.4-mini \
    --judge-base-url=https://api.openai.com   # optional
```

`--judge=openai` runs a single model. `--judge=ensemble` runs the primary
model alongside `gpt-4o-mini` in parallel and majority-votes (split → TIE) per
§7.8 position-bias mitigation. The driver echoes only the SHA-256 prefix of
the API key to stderr.

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

| Field        | Plugin      | Notes                                                        |
| ------------ | ----------- | ------------------------------------------------------------ |
| `tokens_in`  | `pageindex` | LLM input tokens consumed by `file_search` (Phase 2).        |
| `tokens_out` | `pageindex` | LLM output tokens (Phase 2).                                 |
| `llm_calls`  | `pageindex` | Number of LLM round-trips (Phase 2).                         |
| `truncated`  | `pageindex` | `true` when a per-call budget capped the response (Phase 2). |

`rag_plugin` does not emit `tokens_*` or `llm_calls` because LLM calls are not in its
hot path; embedding cost is reported through the existing embedder telemetry and is
attributed to the user's own credentials.

The resolved plugin name is recorded on every request log entry regardless of plugin.

## 9. Troubleshooting

| Symptom                                                                                   | Likely cause                                                                                                     | Action                                                                                            |
| ----------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------- |
| Tool returns `INVALID_ARGUMENT` with `available_plugins=[...]`                            | Caller passed an unknown plugin name (typo, version mismatch, future plugin not yet shipped).                    | Pass one of the listed names, or omit the field to use `default_plugin`.                          |
| Tool returns `NOT_FOUND` with hint "path exists under plugin=A; pass plugin=A to read it" | Path was written via plugin A and the current call is pinned to plugin B (or vice versa).                        | Re-issue the call with `plugin=A`. The manager does not silently cross plugins by design.         |
| Tool returns `INVALID_ARGUMENT` for `plugin="pageindex"` with hint about `llm.api_key`    | `pageindex_plugin` is not registered because `settings.mcp.tools.memory.plugins.pageindex.llm.api_key` is empty. | Set the api_key (Section 6.1) and restart, or drop the `plugin` field to route via default.       |
| Single startup WARN about deprecated `settings.mcp.files.*`                               | Legacy-config shim translated old keys.                                                                          | Move keys to `settings.mcp.tools.memory.plugins.rag.*` before the shim is removed (post-Phase 3). |
| Two routing layers seem active                                                            | There is no second layer. The only inputs are the per-call `plugin` argument and `default_plugin`.               | Confirm by reading the request log entry's resolved-plugin field.                                 |
| PageIndex trees are all `degraded` or every PageIndex call fails | The provider cannot serve the Responses API as configured: it rejects `max_output_tokens`, has no quota for the key, or (before origins were mapped to `/v1`) `llm.base_url` reached the gateway's web UI. | Check `pageindex.llm.respond failed` in the logs, then run `PAGEINDEX_CONTRACT_BASE_URL=<llm.base_url> PAGEINDEX_CONTRACT_API_KEY=<key> go test -run TestLiveResponsesProviderContract ./internal/mcp/memory/plugins/pageindex`. |

## 10. References

- Design proposal — [../proposals/mcp_memory_plugin_manager.md](../proposals/mcp_memory_plugin_manager.md)
- Architecture overview — [../arch/mcp_memory.md](../arch/mcp_memory.md)
- File-tool manual — [mcp_files.md](mcp_files.md)
- Memory tool manual — [mcp_memory.md](mcp_memory.md)
- Eval runbook — [../eval/README.md](../eval/README.md)
- Plugin requirements — [../requirements/mcp_memory_plugins.md](../requirements/mcp_memory_plugins.md)
