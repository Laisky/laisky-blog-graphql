---
title: MCP memory plugin manager (rag_plugin + pageindex_plugin)
status: draft
owner: mcp team
created: 2026-05-05
updated: 2026-05-06
v1_runtime: pure-Go, in-process; pure-Go go.mod additions only, no cgo, no external binary (see §0)
---

# MCP Memory Plugin Manager — Change Manual

## 0. Decision (v1 runtime)

**v1 is pure Go, in-process.** The PageIndex algorithm is reimplemented in Go and runs
in the existing MCP server process. The only network call the plugin makes is HTTPS to
OpenAI's Responses API.

Out of scope for v1, with the build refusing to link against any artifact that would
introduce them:

- No external language runtime, no `pip` dependency, no `requirements.txt` shipped in
  the deploy artifact, no `package.json`, no JVM, no Ruby — nothing outside the Go
  toolchain.
- No cgo. No `llama.cpp`, no ONNX runtime, no native PDF binding.
- No subprocess, no Unix-domain or TCP IPC socket, no proto-generated stubs, no extra
  container in the Compose / k8s manifest.
- No fallback path that re-introduces an external runtime, no compile-time flag that
  enables one, no `if buildtag.<lang>` in the codebase.
- No vendored upstream code at runtime: `/home/laisky/repo/3rd/PageIndex` is consulted
  as an algorithmic reference (prompt shapes, knob defaults, JSON tree schema) only —
  it is never imported, executed, or shipped.

Every "in-process" and "Responses-API only" claim downstream (§1.3, §1.4, §1.5, §2.6,
§3.3, §4.3, §6 A6/A10, §8 Phase 2) resolves back to this section. The `go.mod`
additions in §4.2 are the *complete* set of new runtime dependencies — every entry is
pure Go with stdlib-only transitive deps.

Reopening any of the above is a v2 conversation, not a v1 patch. A future proposal that
argues for an external runtime must (a) supersede this document, not amend it, and (b)
re-derive its own §3 risk analysis from scratch — the risks listed below are sized for
an in-process Go implementation and do not transfer.

## 1. Background

### 1.1 Current state

The MCP "agent memory" surface is the `file_*` toolset, used by AI agents as a durable shared scratchpad
(`/users.md`, `/arch.md`, `/tasks/*.md`, etc.). The surface is fixed:

- `file_stat`, `file_read`, `file_write`, `file_delete`, `file_rename`, `file_list`, `file_search`.

The implementation is one monolithic stack:

| Concern         | Location                                                                                                              |
| --------------- | --------------------------------------------------------------------------------------------------------------------- |
| Tool wiring     | [internal/mcp/server.go:204-452](../../internal/mcp/server.go#L204-L452)                                              |
| Tool adapters   | [internal/mcp/tools/file_*.go](../../internal/mcp/tools/), driven by the [FileService](../../internal/mcp/tools/file_service.go) interface |
| Concrete engine | [internal/mcp/files/service.go](../../internal/mcp/files/service.go) + ~30 sibling files                              |
| Storage         | Postgres tables `mcp_files`, `mcp_file_chunks`, `mcp_file_chunk_embeddings`, `mcp_file_chunk_bm25`, `mcp_file_index_jobs`, `mcp_file_versions` ([migration.go:61-196](../../internal/mcp/files/migration.go#L61-L196)) |
| Retrieval      | Hybrid (pgvector + BM25 + optional Cohere rerank) in [service_search.go:26-150](../../internal/mcp/files/service_search.go#L26-L150) |
| Embeddings     | OpenAI-compatible client [internal/mcp/rag/openai_embedder.go](../../internal/mcp/rag/openai_embedder.go)              |
| Async ingest   | [index_worker.go](../../internal/mcp/files/index_worker.go) consuming `mcp_file_index_jobs`                            |
| Settings       | `settings.mcp.files.*` and `settings.mcp.tools.*` — see [files/settings.go](../../internal/mcp/files/settings.go), [internal/mcp/settings.go](../../internal/mcp/settings.go) |

The `tools.FileService` interface ([file_service.go:10-18](../../internal/mcp/tools/file_service.go#L10-L18)) is the
only abstraction the tool layer depends on. Today it has exactly one implementation
(`*files.Service`) injected in [cmd/api.go:408-447](../../cmd/api.go#L408-L447).

### 1.2 Why change

The current "RAG-style" stack is good at small, even-grained text spans (notes, code, conversation logs)
but degrades on long, hierarchically structured documents (PDFs, manuals, book-length markdown):

- Fixed-byte chunks fragment headings, tables, and cross-references.
- Embedding-similarity ranking conflates "looks like" with "answers the question."
- There is no native "go to section X / fetch pages 12–15" path.

[VectifyAI / PageIndex](https://github.com/VectifyAI/PageIndex) (cloned locally at
`/home/laisky/repo/3rd/PageIndex`) takes the opposite approach — **vectorless,
reasoning-driven retrieval**. We treat it as an *algorithmic reference* to port, not as
a runtime dependency (§1.4):

- Indexing turns each document into a hierarchical JSON tree (Table-of-Contents nodes with
  `node_id`, `title`, `start_index`/`end_index`, LLM summary). Knobs:
  `toc_check_page_num=20`, `max_page_num_each_node=10`, `max_token_num_each_node=20000`.
  Our Go port preserves these knob names and defaults bit-for-bit (§2.4) so any future
  scorecard comparison against upstream is meaningful.
- Retrieval exposes three primitives — `get_document`, `get_document_structure`,
  `get_page_content(pages="5-7,12")` — to an LLM agent that traverses the tree.
- Persistence is plain JSON. Upstream uses a workspace dir (`workspace/{doc_id}.json` +
  `_meta.json`); we keep the JSON shape but persist via `SystemFS` (§2.6.3) instead of a
  separate filesystem tree — no `workspace_root` to provision.
- No vector DB, no embeddings. Confirmed via
  `grep -r 'embedding\|pgvector\|chroma\|faiss\|pinecone'` in the upstream clone (returns
  nothing).
- v1 takes nothing from the upstream runtime — only its prompt shapes, knob defaults, and
  JSON tree schema. The Go-side dependency set is catalogued in §2.6.4 (`pdfcpu`/`dslipak`
  for PDF parsing, `yuin/goldmark` for markdown, `openai-go` for the Responses API, no
  equivalent needed for upstream config-loader libraries — Go config goes through the
  existing `settings.yml` machinery).

### 1.3 Goal

Refactor the memory backend behind a **plugin contract** so different engines can be plugged
in per-tenant or per-project without changing the public MCP tool schemas. Ship two plugins on
day one:

- **`rag_plugin`** — the existing Postgres + pgvector + BM25 + rerank stack, behavior-preserving.
- **`pageindex_plugin`** — a new vectorless, tree-based engine. Pure-Go reimplementation of
  the PageIndex algorithm; upstream `VectifyAI/PageIndex` is the algorithmic reference, not a
  runtime dependency. See §1.4 for the v1 dependency boundary.

A `PluginManager` selects the active plugin at request scope using two inputs only: an
optional per-call `plugin` argument on the tool input (§2.4.1) and a single global
`default_plugin` setting. There is no per-project, per-API-key, or per-tenant override
layer — agents pin a backend per call when they need to, and operators flip the global
default when they want to migrate everyone. This is a deliberate simplicity bet (§2.4).

### 1.4 Non-goals

- No change to the public MCP tool schemas beyond the additive optional `plugin` field
  (§2.4.1).
- No change to `memory_*` session-memory tools (`memory_before_turn`, `memory_after_turn`,
  `memory_run_maintenance`, `memory_list_dir_with_abstract`) — those layer on top of the plugin
  via `FileService` and need no work beyond compile-time fixes.
- No automatic migration of existing tenant data into PageIndex format. New plugin starts empty
  and is opt-in.
- **v1 is pure Go, in-process.** The PageIndex algorithm is reimplemented in Go (see §2.6,
  §3.3). Only HTTPS to OpenAI's Responses API leaves the process. Upstream
  `/home/laisky/repo/3rd/PageIndex` is an **algorithmic reference** — we cite its prompt
  shapes, knob defaults, and JSON tree schema; nothing from it runs in production. The
  full out-of-scope list is in §0 and is not relitigated here.
- **Not a vector-DB replacement.** Per the 2026 industry consensus (see §1.5), PageIndex is
  *tier-2* retrieval — invoked after candidate document(s) are chosen. `rag_plugin` remains
  the right answer for at-scale coarse retrieval across an unbounded corpus. The two plugins
  are complementary, selected per-call by the agent or per-server by the operator (§2.4),
  not adversaries.

### 1.5 Industry context (2026 sanity check)

Four observations from a 2026 survey of agent-memory and vectorless-RAG systems shape the
design choices below; they are recorded here so future readers can audit the assumptions.

- **Pure-Go in-process LLM orchestration is production-proven; v1 commits to it.**
  ByteDance's [Eino](https://github.com/cloudwego/eino) runs in Doubao and TikTok
  recommendation traffic as a pure-Go agent framework. On the TS/Node side, teams report
  end-to-end p95 dropping from ~2 450 ms to ~1 240 ms purely by removing the Python
  boundary
  ([2026 Mastra adoption report](https://dev.to/jim_l_efc70c3a738e9f4baa7/i-switched-from-langgraph-to-mastra-for-my-typescript-agents-18-hours-vs-41-nah)).
  Anthropic's [memory tool](https://platform.claude.com/docs/en/agents-and-tools/tool-use/memory-tool)
  is explicitly a *client-side* contract — the model emits tool calls and the host executes
  them — so a Go host is first-class. The §1.4 commitment is therefore the cheap option,
  not the constrained one. §2.6 spells out the indexer.
- **Memory backends behind a narrow tool surface is the norm** (Mem0: `add/search/update/delete`
  with 19 vector backends + 2 graph backends; Letta, Zep, LangGraph Memory, Anthropic memory
  tool follow the same shape). Per-tenant plugin selection is routed in the host *before*
  tool dispatch — the backend never sees tenant identity except as opaque scope IDs.
- **Reserved prefixes inside a user-visible FS namespace are an acknowledged anti-pattern**
  at scale (AWS S3 multi-tenant guidance; documented prefix/RLS leakage modes). The fix is a
  separate **system namespace** the user API cannot address — enforced in the data path, not
  by convention. §2.6.3 uses this pattern for plugin-internal artifacts (tree JSON, catalog).
- **OpenAI's `/v1/responses` (Responses API) is the single LLM contract.** The provider-native
  Responses API ships strict JSON-Schema structured output, server-side conversation state,
  and incremental streaming in one endpoint. We deliberately scope v1 to **external API
  only**, **Responses-API format only**: no `litellm`-equivalent abstraction, no local-
  inference engines, no Anthropic SDK, no reflection-based wrappers (`instructor-go`,
  `langchaingo`). The official [openai-go](https://github.com/openai/openai-go) SDK is the
  one runtime LLM dependency; the indexing model is `gpt-5.4-mini` and the (rag-side)
  embedding model is `text-embedding-3-small` (configurable, but those are the only
  defaults validated in CI). Vendors that implement the Responses API with their own keys
  work by overriding `base_url` only — adding a new model contract (chat-completions,
  Anthropic Messages, etc.) is explicitly out of scope for v1. §2.6.5 builds on this.

## 2. Design

### 2.1 The plugin contract

Promote `tools.FileService` to a stable, top-level interface and rename it for clarity. A new
package `internal/mcp/memory/plugin` will own the contract and the manager:

```go
// internal/mcp/memory/plugin/plugin.go (new)
package plugin

type Plugin interface {
    // Identity returned in /healthz and logged on every call.
    Name() string                             // "rag" | "pageindex" | ...
    Capabilities() Capabilities               // see §2.2

    // Mirror of today's tools.FileService surface.
    Stat   (ctx, auth, project, path) (StatResult, error)
    Read   (ctx, auth, project, path, off, n) (ReadResult, error)
    Write  (ctx, auth, project, path, content, encoding, offset, mode) (WriteResult, error)
    Delete (ctx, auth, project, path, recursive) (DeleteResult, error)
    Rename (ctx, auth, project, src, dst, overwrite) (RenameResult, error)
    List   (ctx, auth, project, path, depth, limit) (ListResult, error)
    Search (ctx, auth, project, query, pathPrefix, limit) (SearchResult, error)

    // Lifecycle.
    Start(ctx) error
    Stop (ctx) error
}
```

`StatResult` … `SearchResult` are the existing `files.*Result` value types, **moved** into
`internal/mcp/memory/plugin/types.go` (re-exported back into `files` for backward source
compatibility — internal-only callers will be retargeted).

`tools.FileService` becomes a thin alias of `plugin.Plugin` to preserve adapter code unchanged
on day one; we drop the alias once adapters are migrated.

### 2.2 Capability advertisement

Different plugins legitimately differ. Each plugin reports:

```go
type Capabilities struct {
    SearchModes       []SearchMode  // rag: {"hybrid","semantic","lexical"}; pageindex: {"tree-reasoning"}
    SupportsRandomIO  bool          // both: true — pageindex routes raw reads through userFS (§2.6.1).
                                    //   The .pdf/.md OVERWRITE@offset rejection in §3.4 is a per-path
                                    //   refinement surfaced via INVALID_ARGUMENT, not a capability flip.
    SupportsRename    bool
    SupportsVersions  bool          // rag: true (mcp_file_versions); pageindex: false in v1
    MaxPayloadBytes   int64
    AsyncIndexing     bool          // rag: true; pageindex: false — indexing is sync, LLM-bound,
                                    //   in-process. The freshness window in C05 is 0 s for pageindex.
    FreshnessWindow   time.Duration // documented end of the C05/R06 contract (rag: 5 s; pageindex: 0)
    Notes             string
}
```

The MCP server uses this only for soft hints surfaced in tool errors; the tool schemas remain
identical, so callers see uniform behavior.

### 2.3 PluginManager

```go
// internal/mcp/memory/plugin/manager.go (new)
type Manager struct {
    plugins       map[string]Plugin    // by Name()
    defaultPlugin Plugin               // settings.mcp.memory.default_plugin
}

// override is the per-call argument from §2.4.1. Empty or "auto" means "use the default".
// auth and project are accepted for logging/metrics but do not influence the resolution.
func (m *Manager) Resolve(ctx context.Context, auth AuthContext, project, override string) (Plugin, error)
func (m *Manager) StartAll(ctx context.Context) error
func (m *Manager) StopAll(ctx context.Context)  error
func (m *Manager) ForName(name string) (Plugin, error)  // for tests + admin tools
```

### 2.4 Settings & resolution rules

Resolution order (first match wins):

1. **Per-call argument** — optional `plugin` field on every `file_*` tool input (see §2.4.1).
2. **Default** (`settings.mcp.memory.default_plugin`, fallback `"rag"`).

That is the entire decision tree. There is **no** per-project routing table, **no**
per-API-key routing table, and **no** per-tenant override map. An earlier draft proposed
a `plugin_overrides.{project,api_key_hash}` block; it was removed because:

- The per-call `plugin` argument already covers every use case the override map covered
  (an agent that knows a doc lives in PageIndex passes `plugin="pageindex"`; an operator
  that wants a class of project on PageIndex teaches the agent to do that, or flips the
  global `default_plugin`).
- Two routing layers (override map + per-call) is two layers of debugging when an agent
  asks "why did my call go to plugin X?" — the answer is now always "either you passed
  `plugin=X` or the operator set `default_plugin=X`," nothing else.
- A routing map keyed on `api_key_hash` is config-as-database — the key set churns with
  the tenant set, the YAML grows unbounded, and there is no schema to validate it. If we
  ever need per-tenant routing, it belongs in a real datastore with admin tooling, not in
  `settings.yml`.

Operators who want a class of project on a non-default plugin set the agent's prompt to
emit `plugin="pageindex"` for those projects (the natural place for that policy — the
agent already knows which project it is writing to). Operators who want everyone moved
flip `default_plugin`.

#### 2.4.1 Per-call `plugin` argument

Every memory-surface tool (`file_stat`, `file_read`, `file_write`, `file_delete`, `file_rename`,
`file_list`, `file_search`) gains a single **optional** input field:

```json
{
  "plugin": {
    "type": "string",
    "enum": ["rag", "pageindex", "auto"],
    "default": "auto",
    "description": "Memory backend to handle this call. 'auto' (default) uses the resolution rules in settings; explicit values pin this single call to that backend."
  }
}
```

Semantics:

- **Omitted or `"auto"`** — fall through to `default_plugin`. **Today's default ⇒
  `rag_plugin`.** Existing callers see no change.
- **Explicit name** (`"rag"` / `"pageindex"`) — pin this single invocation to that
  backend regardless of `default_plugin`. Useful for: agents that know which backend a
  doc lives in; A/B comparison of the two engines on the same query; force-routing a
  `file_write` of a long PDF into PageIndex even when the server default is RAG.
- **Unknown name** — return `INVALID_ARGUMENT` with `available_plugins=[...]` in
  `structuredContent` so the caller can self-correct.
- **Cross-plugin reads** — if a path was written via plugin A but the call specifies plugin B
  and the path does not exist there, the response is `NOT_FOUND` with a hint
  `("path exists under plugin=A; pass plugin=A to read it")`. We do not silently fall back
  across plugins (that would defeat tenant isolation between engines and surprise the caller).
- **Mutations are sticky** — a `file_write` under plugin X stores the path under X *only*. There
  is no implicit migration. A subsequent `file_read` without an explicit `plugin` will resolve
  to `default_plugin`; if that is a different plugin, the read returns `NOT_FOUND` with the
  hint above.

The field is forwarded by the tool adapter into `Manager.Resolve(ctx, auth, project, override)`
and recorded on the request log entry so per-plugin call volumes are observable.

New settings keys:

```yaml
settings:
  mcp:
    memory:
      default_plugin: "rag"            # "rag" | "pageindex" — the only routing knob
      plugins:
        rag: {}                         # current settings.mcp.files.* re-rooted under here
        pageindex:
          # Indexer (Go, in-process; see §1.4).
          indexer:
            timeout_index:    "5m"      # ceiling for a single document index call
            timeout_query:    "60s"
            max_concurrency:  8         # bounded LLM-call fan-out per indexing job
            retry:
              max_attempts:    10       # parity with upstream PageIndex defaults
              initial_backoff: "250ms"
              max_backoff:     "8s"
            cache:
              enabled:         true
              path:            "/var/lib/laisky/pageindex-cache.bbolt"
              max_size_bytes:  1073741824  # 1 GiB; LRU evict
          # External LLM API only. Single contract: OpenAI Responses API
          # (POST /v1/responses). No Anthropic SDK, no local-inference endpoint,
          # no `litellm`-equivalent multi-provider router (§2.6.5).
          llm:
            indexing_model:  "gpt-5.4-mini"
            retrieve_model:  "gpt-5.4-mini"
            api_key:         ""        # OpenAI API key, configured directly in this file.
                                       # Config-file confidentiality is handled out-of-band
                                       # (file ACLs / secret-management tooling), so the
                                       # plugin reads the key as-is without env-var indirection.
            base_url:        ""        # empty = api.openai.com; vendors implementing
                                       # the Responses API with their own keys override here.
          # Algorithmic knobs, parity with upstream PageIndex defaults.
          algo:
            toc_check_page_num:        20
            max_page_num_each_node:    10
            max_token_num_each_node:   20000
            generate_node_summary:     true
            generate_doc_description:  true
          # Search-time budgets — bounded mini-agent over the cached tree (§2.6.2).
          tree_query:
            max_steps:        8
            max_tokens:       20000
            candidate_docs:   5
          # Pure-Go PDF parser selection (§2.6.4). Default `pdfcpu` for Apache-2.0
          # license + outline support; `dslipak` available as a text-only fallback
          # if a tenant's corpus regresses on pdfcpu's text quality.
          pdf:
            text_parser:    "pdfcpu"   # "pdfcpu" | "dslipak"
            outline_parser: "pdfcpu"   # only pdfcpu exposes outlines today
```

Existing `settings.mcp.files.*` keys move under `settings.mcp.memory.plugins.rag.*` with a
deprecation shim that reads the old keys for one minor version and logs a one-time WARN.

### 2.5 `rag_plugin` — wrapping the existing engine

A new package `internal/mcp/memory/plugins/rag` wraps `*files.Service`:

```go
// internal/mcp/memory/plugins/rag/plugin.go (new)
type Plugin struct{ inner *files.Service }

func New(deps Deps) (*Plugin, error)            // forwards to files.NewService
func (p *Plugin) Name() string                  { return "rag" }
func (p *Plugin) Capabilities() ...             // hybrid+lexical+semantic, async, versions
// Stat/Read/Write/Delete/Rename/List/Search delegate to inner.
```

No behavior changes. All tables, jobs, embedders, rerankers stay where they are — the wrapper
is ~150 lines of glue.

### 2.6 `pageindex_plugin` — new engine

Located at `internal/mcp/memory/plugins/pageindex`. The PageIndex algorithm — TOC
detection → tree extraction → recursive node refinement → optional summarization — is
reimplemented in Go and runs in-process per §1.4.

The upstream Python repo at `/home/laisky/repo/3rd/PageIndex` is the **algorithmic
reference**: we cite it for prompt shapes, knob defaults
(`toc_check_page_num=20`, `max_page_num_each_node=10`, `max_token_num_each_node=20000`),
and the JSON tree schema. Implementation details (file layout, retry strategy, concurrency
primitives) follow Go idioms.

Three design choices distinguish this from a naïve port:

- **All persistence rides on the existing fileio.** Raw bytes (the user's PDF/MD) are stored
  via the *same* `*files.Service` that backs `rag_plugin`'s user-facing layer, so
  `file_read`/`file_stat`/`file_list` against a `pageindex_plugin`-backed project behave
  identically to today for those verbs. PageIndex's own metadata (per-doc tree JSON, the
  `_meta.json` catalog, the path↔doc_id index) lives in a **separate system namespace** the
  user API cannot address (see §2.6.3). No `workspace_root` directory, no new
  `mcp_pageindex_docs` table, no new on-disk layout.
- **Indexing runs in-process with bounded fan-out.** A single `Indexer.Index(ctx, doc)`
  call drives the whole pipeline: page extraction (pure-Go PDF parser, §2.6.4), TOC
  detection / tree construction / verification / summarization (LLM calls fanned out via
  `golang.org/x/sync/errgroup` + `golang.org/x/sync/semaphore`, capped at
  `indexer.max_concurrency`), and returns a `*Tree` ready to persist via `SystemFS`.
  Progress is published on an in-process buffered `chan Progress`, surfaced to MCP
  callers via `report_progress` notifications when the request uses the streamable-HTTP
  transport (MCP 1.25.1+).
- **LLM access is via the OpenAI Responses API only.** A single dependency
  (`github.com/openai/openai-go`) handles indexing prompts and the retrieval-time
  mini-agent. Default indexing/retrieve model: `gpt-5.4-mini`. The `LLM` interface in
  §2.6.5 is a thin adapter over `client.Responses.New(...)` — there is no provider
  routing, no Anthropic SDK, no local-model fallback, no `langchaingo` /
  `instructor-go`. Vendors that re-implement the Responses API can be reached by
  overriding `base_url`, but supporting a different *model contract* (chat-completions,
  Anthropic Messages) is out of scope for v1.

End-to-end data flow for a `file_write(.pdf)` followed by `file_search` under
`pageindex_plugin`:

```
                 MCP request
                      |
                      v
              +--------------+
              | tool adapter |
              +------+-------+
                     |
                     v
        +------------------------+
        |  plugin.Manager        |
        |  Resolve(...)→pageindex|
        +------------+-----------+
                     |
                     v
        +------------------------+
        | pageindex.Plugin       |
        |  userFS sysFS indexer  |
        +------+----------+------+
               |          |
       write   |          | search
               v          v
       +-------+--+   +---+----------+
       | userFS   |   | sysFS.Read   |
       | (mcp_*)  |   |  tree JSON   |
       +-----+----+   +---+----------+
             |            |
             v            v
       +-----+----+   +---+--------+
       | Indexer  |   | search_loop|
       |  Index() |   |  → LLM →   |
       |  errgroup|   |  GetPageContent (in-mem)
       |  + sem   |   +---+--------+
       +-----+----+       |
             |            v
       openai-go      synthesized
       Responses API  ChunkEntry[]
       (gpt-5.4-mini)
             |
             v
       sysFS.Write tree JSON
       sysFS.Write index.json (path↔doc_id)
```

The only network egress is the OpenAI Responses-API call.

#### 2.6.1 Mapping `FileService` → PageIndex (fileio-backed)

Notation: `userFS` is the user-facing fileio (the same instance `rag_plugin` wraps);
`sysFS` is `userFS` accessed through the system-namespace handle (§2.6.3).

| Tool          | Behavior under `pageindex_plugin`                                                                                                                                                                                                                |
| ------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `file_write`  | 1) `userFS.Write(project, path, content, mode)` — exactly today's path. 2) If `path` ends with `.pdf`/`.md`: invoke `Indexer.Index(ctx, content, opts)` in-process (bounded LLM fan-out per §2.6.4); on success, `sysFS.Write("pageindex/<doc_id>.json", treeJSON, TRUNCATE)` and update `sysFS:"pageindex/_meta.json"` plus the path↔doc_id mapping in `sysFS:"pageindex/index.json"`. Other extensions (`.txt`, `.json`): step 1 only. |
| `file_read`   | `userFS.Read(...)` — unchanged, including offsets. Random IO works for every path under this plugin since user content is in the user namespace.                                                                                                  |
| `file_stat`   | `userFS.Stat(...)` — unchanged.                                                                                                                                                                                                                  |
| `file_list`   | `userFS.List(...)` — unchanged. **System namespace is invisible** (enforced at the SQL layer, not by path filter — see §2.6.3).                                                                                                                  |
| `file_delete` | `userFS.Delete(...)` then if a mapping exists: `sysFS.Delete("pageindex/<doc_id>.json")` and update `sysFS:"pageindex/index.json"`/`pageindex/_meta.json`.                                                                                       |
| `file_rename` | `userFS.Rename(...)` then update the path↔doc_id mapping in `sysFS:"pageindex/index.json"`. Tree JSON content unchanged.                                                                                                                          |
| `file_search` | The interesting case — see §2.6.2.                                                                                                                                                                                                               |

Two non-obvious consequences of letting `userFS` handle steps 1 of write/delete/rename:

- **Versioning, MinIO replication, advisory locks, audit logs** — all already implemented in
  `*files.Service` ([service.go](../../internal/mcp/files/service.go),
  [lock.go](../../internal/mcp/files/lock.go),
  [service_versions.go](../../internal/mcp/files/service_versions.go)) — apply uniformly.
  No reimplementation needed in `pageindex_plugin`.
- **`rag_plugin`'s indexing pipeline (chunks/embeddings/BM25) also runs on these writes**
  unless explicitly disabled per-project. That's wasteful — the user picked PageIndex *because*
  they didn't want chunked search. Solution: a new `Capabilities`-driven flag
  `userFS.WriteOpts{ SkipRAGIndex bool }`. When `pageindex_plugin` writes, it sets
  `SkipRAGIndex=true`; the index worker simply does not enqueue jobs for those rows. A nullable
  `mcp_files.skip_rag_index BOOL` column carries the bit. (`rag_plugin`'s wrapper always
  passes `SkipRAGIndex=false`.)

#### 2.6.2 `file_search` under PageIndex

Today's `file_search` returns `[]ChunkEntry{ FilePath, FileSeekStartBytes, FileSeekEndBytes, ChunkContent, Score }`.
Under PageIndex we synthesize this from a tree-reasoning loop:

1. Resolve the candidate doc set: read `sysFS:"pageindex/index.json"` for the
   `(api_key_hash, project)` scope; filter by `path_prefix`.
2. For each candidate (capped by `tree_query.candidate_docs`), run an internal mini-agent
   loop bounded by `tree_query.max_steps` and `tree_query.max_tokens`:
   a. `sysFS.Read("pageindex/<doc_id>.json")` — the cached tree JSON. Pure in-process I/O.
   b. Ask the LLM (`retrieve_model`, default `gpt-5.4-mini`) via the OpenAI Responses API
      "given this tree and this query, list up to N page ranges most likely to contain the
      answer; output JSON `[{pages,reason}]`." The Responses API enforces the JSON Schema
      server-side (`text.format.type=json_schema, strict=true`); the response parses
      directly into a Go struct without reflection-based wrappers (§2.6.5).
   c. For each range, resolve `GetPageContent(doc_id, pages)` against the cached tree —
      this is an in-process JSON traversal of `pages: [{page, content}]`, no I/O.
3. Convert each returned `{page, content}` into a `ChunkEntry` with synthesized byte offsets
   (page-level offsets resolved via the cached `pages` array; markdown uses `line_num`).
   `Score` is a normalized rank from the LLM (fallback: position-based decay).
4. Merge and sort across docs; truncate to `limit`.

This loop is fully encapsulated inside the plugin — the MCP tool schema is unchanged. Cost
and latency are bounded by the budgets above; budgets are tracked by an `atomic.Int64`
counter shared across the goroutines spawned for the candidate-doc fan-out, exposed as
settings, and emitted as billing metadata (`mcp.memory.pageindex.tokens_in`, `tokens_out`,
`llm_calls`).

#### 2.6.3 Persistence layout (system namespace, fileio-backed)

There is **no new on-disk layout, no `workspace_root`, no `mcp_pageindex_docs` table.** All
plugin-internal artifacts are stored as files via the existing `*files.Service`, but in a
**system namespace** invisible to user-facing tools:

```
userFS (namespace = "")                   ← what file_list / file_search return
  <project>/<original/path/with/dirs>     ← exact user bytes (PDF, MD, txt, ...)

sysFS (namespace = "pageindex")           ← never returned by file_list / file_search / file_read
  pageindex/<doc_id>.json                 ← PageIndex tree (nodes + cached page text)
  pageindex/_meta.json                    ← per-tenant per-project catalog
  pageindex/index.json                    ← path → doc_id mapping (replaces mcp_pageindex_docs)
```

System-namespace separation is enforced at the **SQL layer**, not by path-prefix
filtering (which the 2026 anti-pattern guidance flags as leakage-prone):

- New column `mcp_files.system_owner TEXT NOT NULL DEFAULT ''` (empty string = user
  namespace; non-empty = a system owner like `'pageindex'`).
- Every existing user-facing query on `mcp_files`, `mcp_file_chunks`, `mcp_file_index_jobs`,
  `mcp_file_versions` gains an explicit `AND system_owner = ''` clause. Linted via a
  CI-enforced `ast-grep` rule that fails on any new query touching these tables without that
  predicate.
- The `SystemFS` handle internally injects `system_owner = '<owner>'` on every read/write;
  it has its own restricted method surface (no `Search` — system data is keyed lookups only)
  and is not exposed to MCP tools.

The path↔doc_id mapping (formerly `mcp_pageindex_docs`) becomes a single
`sysFS:"pageindex/index.json"` document of shape:

```json
{
  "<project>": {
    "<user/path/relative.pdf>": {
      "doc_id": "ab12...", "type": "pdf",
      "page_count": 142, "indexed_at": "..."
    }
  }
}
```

Mutations are serialized by `*files.Service`'s existing per-path advisory lock on
`pageindex/index.json`, so concurrent writes to different user paths can each contend for
the same lock briefly but never corrupt the file. For high-mutation projects (~ >50 RPS of
indexing writes — far above expected) we would shard `index.json` into
`pageindex/index/<bucket>.json`; the v1 design defers that until measurements demand it.

#### 2.6.4 Indexer

The indexer is a Go package, not a service. It is a single object the plugin holds:

```go
// internal/mcp/memory/plugins/pageindex/indexer.go (new)
package pageindex

type Indexer struct {
    llm    LLM           // §2.6.5 — Responses-API only
    pdf    PDFParser     // pdfcpu by default (§2.6.4.1)
    md     MarkdownParser // yuin/goldmark
    tok    Tokenizer     // tiktoken-go/tokenizer (BPE embedded at build time)
    cache  Cache         // bbolt; key = SHA-256 of (model, prompt, schema)
    sem    *semaphore.Weighted
    cfg    Settings
    metric Reporter      // tokens_in/out, llm_calls, phase timings
}

func (idx *Indexer) Index(
    ctx context.Context,
    docKind Kind,         // KindPDF | KindMarkdown
    bytes  []byte,
    opts   IndexOptions,
    progress chan<- Progress, // optional; nil = silent
) (*Tree, *Stats, error)

func (idx *Indexer) GetPageContent(tree *Tree, ranges []PageRange) ([]Chunk, error) // pure, no I/O
func (idx *Indexer) GetDocumentStructure(tree *Tree) StructureView                  // pure, no I/O
```

The indexer is a Go object owned by the plugin; liveness is the Go process's own.

##### 2.6.4.1 Pipeline

The pipeline mirrors the upstream PageIndex flow, expressed in Go primitives. Counts are
typical for a 200-page PDF; budgets are enforced by a single `atomic.Int64` token counter
seeded from `algo.max_token_num_each_node × N + tree_query.max_tokens`.

| Phase | What runs | LLM calls | Implementation |
| --- | --- | --- | --- |
| 1. Page extraction | `pdf.ExtractTextFile`, `api.Bookmarks` (pdfcpu) or `goldmark` AST walk | 0 | Pure Go |
| 2. TOC detection | `toc_detector_single_page` over first `algo.toc_check_page_num` pages | 1 / page (≤20) | `errgroup` + `sem.Acquire(1)` |
| 3. TOC presence + page-number probe | `detect_page_index` on concatenated TOC pages | 1 | sequential |
| 4. Mode dispatch | with-page-#, no-page-#, or no-TOC branch (parity with upstream `meta_processor`) | 0 | switch/case |
| 5. TOC parsing | `toc_transformer` + `toc_index_extractor` (Mode A) or `add_page_number_to_toc` per group (B) or `generate_toc_init` + `_continue` per 20K-token group (C) | 2–8 | sequential within branch; retried up to 5× on incomplete output |
| 6. Tree assembly | `list_to_tree` flat→hierarchical | 0 | Pure Go |
| 7. Verification | `check_title_appearance` random sample; on accuracy ∈ (60%,100%): `single_toc_item_index_fixer` per failing item | 5–10 | `errgroup` |
| 8. Recursive node expansion | re-enter pipeline on nodes where `node_pages > max_page_num_each_node && node_tokens >= max_token_num_each_node` | bounded by depth ≤ 3 | `errgroup` |
| 9. Optional summarization | `generate_node_summary` per leaf, `generate_doc_description` once | 5–25 | `errgroup` + `sem` |

Each LLM call uses `LLM.Respond(ctx, req)` (§2.6.5), wrapped in
`avast/retry-go/v4` with exponential backoff (`250 ms × 2^n` up to 8 s, max 10 attempts —
parity with upstream's retry budget). Cancellation: `context.Context` propagates from the
MCP request through `errgroup.WithContext`; sibling LLM calls cancel on first error.

##### 2.6.4.2 Concurrency

```go
func (idx *Indexer) verifyTOC(ctx context.Context, tree *Tree) error {
    g, gctx := errgroup.WithContext(ctx)
    for _, node := range tree.Sample(idx.cfg.VerifySampleN) {
        node := node
        if err := idx.sem.Acquire(gctx, 1); err != nil { return err }
        g.Go(func() error {
            defer idx.sem.Release(1)
            if idx.budget.Load() <= 0 { return ErrBudgetExceeded }
            ok, used, err := idx.checkTitleAppearance(gctx, node)
            idx.budget.Add(-int64(used))
            if err != nil { return err }
            if !ok { node.MarkForFix() }
            return nil
        })
    }
    return g.Wait()
}
```

`indexer.max_concurrency` (default 8) sizes `idx.sem`. The same primitive carries through
node summarization and recursive expansion — there is no goroutine-per-node explosion.

##### 2.6.4.3 Caching

Every LLM call's `(model, prompt, schema, params)` tuple is hashed to a 32-byte key; the
response (raw bytes + token usage) is stored in a local `bbolt` database under
`indexer.cache.path`. A re-index of the same PDF (same algorithm version) hits the cache
for every prompt. Cache size is bounded by `indexer.cache.max_size_bytes` with LRU
eviction; the cache is keyed by an algorithm-version constant so a prompt-template change
implicitly invalidates old entries.

##### 2.6.4.4 Progress

`Index` writes one `Progress{phase, percent, tokens_in, tokens_out, llm_calls}` event per
phase transition and on every `errgroup` member completion. Callers that pass `progress=nil`
get nothing; callers that pass a buffered channel can forward events to:

- The MCP `report_progress` notification (streamable-HTTP transport, MCP 1.25.1+) — the
  default path for a client that opted into progress.
- A `zap.Logger` field — the always-on path.
- Per-call billing metadata aggregated into the tool response's `structuredContent`.

The plugin awaits the final `Tree` before returning to the MCP tool layer, so callers that
*don't* opt into streamable-HTTP simply see the tool response take longer.

##### 2.6.4.5 Pure-Go PDF parsing

`PDFParser` is satisfied by [pdfcpu](https://github.com/pdfcpu/pdfcpu) (Apache-2.0, 8.6k
stars, active May 2026). `api.ExtractTextFile` returns per-page text; `api.Bookmarks`
returns the recursive `Bookmark{Title, PageFrom, Children}` outline tree (maps directly
onto the upstream `structure: [...]` shape); `api.PageCountFile` and `api.PDFInfo` cover
metadata. License is commercial-safe; pinned to a tagged release (no master tracking).

A second implementation, [`dslipak/pdf`](https://pkg.go.dev/github.com/dslipak/pdf),
is registered as a fallback for tenants whose corpus regresses on pdfcpu's text quality;
the choice is per-plugin-instance via `pdf.text_parser` and is invisible to MCP callers.
**[UniDoc / unipdf](https://unidoc.io) is rejected** — its AGPL clause triggers on
network use, which would force AGPL onto the entire MCP server.

`MarkdownParser` is [`yuin/goldmark`](https://github.com/yuin/goldmark) — still the 2026
default with no challenger, CommonMark-compliant, no cgo, deps stdlib-only.

##### 2.6.4.6 Prompt invariance vs. upstream

`prompts.go` carries the 13 prompt templates ported from upstream PageIndex
(`page_index.py` + `utils.py`). Each template is a Go `text/template` string with the
same body as upstream — only the variables and the surrounding output-schema directive
change. A `golden_prompts_test.go` golden-fixture test asserts that the rendered prompt
for canned inputs is byte-identical to a frozen reference (regenerated only when a port
of an upstream improvement is intentional). This makes upstream-vs-port quality
regressions detectable: if the §7 scorecard moves and prompts changed, we know which
direction to look.

The Responses-API-specific glue (`text.format.type=json_schema, strict=true`,
`max_output_tokens`) is appended *outside* the rendered prompt body, so the body itself
remains comparable to the upstream Python prompts even though the call shape differs.

#### 2.6.5 LLM interface (Responses API only)

```go
// internal/mcp/memory/plugins/pageindex/llm.go (new)
package pageindex

// LLM is the single contract used by both indexing prompts (§2.6.4.1) and the
// retrieval mini-agent (§2.6.2). It wraps OpenAI's Responses API only.
type LLM interface {
    // Respond performs a single Responses-API call and returns the parsed result.
    // The schema is enforced server-side via text.format.type=json_schema,strict=true.
    Respond(ctx context.Context, req Request) (*Response, error)

    // CountTokens uses the embedded BPE (tiktoken-go/tokenizer) to estimate prompt
    // cost before the call. Counts every InputItem in req.Input under req.Model.
    CountTokens(ctx context.Context, req Request) (int, error)
}

// InputItem mirrors one entry in the Responses-API "input" array. We use the
// minimal {role, content} shape; tool-use items can be added later if needed.
type InputItem struct {
    Role    string // "system" | "user" | "assistant"
    Content string // textual content; the openai-go SDK splits it into content parts
}

type Request struct {
    Model        string          // e.g., "gpt-5.4-mini"
    Input        []InputItem
    Schema       json.RawMessage // JSON Schema; nil = free-form text
    MaxOutTokens int
    Temperature  float32
    PromptHash   [32]byte        // SHA-256 of (Model, Input, Schema, params) for the bbolt cache (§2.6.4.3)
}

type Response struct {
    Output json.RawMessage // schema-validated JSON when Request.Schema != nil
    Text   string          // textual fallback when Request.Schema == nil
    Usage  Usage           // {InputTokens, OutputTokens, TotalTokens}
}

type Usage struct{ InputTokens, OutputTokens, TotalTokens int }
```

The single concrete implementation is `openaiLLM`, a ~80-line wrapper over
`github.com/openai/openai-go`'s `Responses.New`. There is no Anthropic implementation in
v1, no chat-completions adapter, no `langchaingo`/`instructor-go` shim. Vendors that
re-implement the Responses API at a different host are reachable via `base_url`; that's
the entire portability story.

Token counting uses [`github.com/tiktoken-go/tokenizer`](https://github.com/tiktoken-go/tokenizer)
(BPE vocabularies embedded at build time — no runtime download, no `/tmp` cache, no cgo).
Rate limiting is a per-key `golang.org/x/time/rate.Limiter`; retry policy is
`avast/retry-go/v4` honoring `Retry-After`. Both are wrapped inside `openaiLLM`, so the
MCP-side caller never sees them.

The rag plugin's embedding pathway, when it needs an OpenAI embeddings call, defaults to
`text-embedding-3-small` and goes through the same `openai-go` SDK (separate `Embeddings`
client, not via this `LLM` interface — embeddings are not LLM-style structured calls).

### 2.7 Wiring changes

- [cmd/api.go:408-447](../../cmd/api.go#L408-L447): replace direct `files.NewService`
  injection with `plugin.Manager.MustBuild(cfg)`. The manager constructs each enabled plugin
  (today: rag always on; pageindex on if config block present) and starts them.
  - The single `*files.Service` instance is built once and **shared** by both plugins:
    `rag_plugin` wraps it directly; `pageindex_plugin` receives it as `userFS` and a
    `SystemFS` handle scoped to `system_owner="pageindex"`.
  - **Pageindex enablement gate.** `pageindex_plugin` is constructed only if its config
    block is present **and** `llm.api_key` is a non-empty string at startup. If the block
    is present but `llm.api_key` is empty, `MustBuild` fails fast with a startup error
    naming the missing config key. If the block is absent entirely, `pageindex_plugin`
    is silently not registered — explicit `plugin="pageindex"` calls then return
    `FAILED_PRECONDITION` per A10.
  - **Pageindex sub-construction.** Inside the manager, building `pageindex_plugin`
    threads the following one-time singletons:
    1. `openaiLLM` — built from `llm.{indexing_model,retrieve_model,api_key,base_url}`
       wrapped in `golang.org/x/time/rate.Limiter` (keyed by `sha256(llm.api_key)` so log
       lines and metric labels never carry the raw key) and `avast/retry-go/v4`.
    2. `tiktoken-go/tokenizer` (singleton) — selected for the configured `indexing_model`
       at startup; embedded BPEs mean no network call during init.
    3. `bbolt.DB` — opened at `indexer.cache.path`, single shared DB across goroutines
       (bbolt does its own MVCC).
    4. `semaphore.NewWeighted(indexer.max_concurrency)` — a single instance shared by
       every concurrent `Indexer.Index` and `Search` call.
    5. `Indexer{llm,pdf,md,tok,cache,sem,cfg}` — the orchestrator, constructed last so
       `Plugin{userFS,sysFS,indexer,cfg}` is just a struct literal at the end.
- [internal/mcp/server.go:204-452](../../internal/mcp/server.go#L204-L452): each `file_*` tool
  receives the `Manager`, not a single `FileService`; per-call it does
  `mgr.Resolve(ctx, auth, project, req.Plugin)` and forwards. The `Plugin` field is decoded
  from the new optional input parameter (§2.4.1).
- [internal/mcp/tools/file_*.go](../../internal/mcp/tools/): each tool's `Definition()` adds
  the `plugin` field to its input schema; each `Handle()` decodes it into `req.Plugin string`
  and passes it through. A shared helper `tools.parsePluginOverride(raw string) (string, error)`
  validates the value (`""`/`"auto"`/known plugin name).
- [internal/mcp/files/service.go](../../internal/mcp/files/service.go) and the SQL files in
  [internal/mcp/files/](../../internal/mcp/files/): every read/write touching `mcp_files`,
  `mcp_file_chunks`, `mcp_file_chunk_embeddings`, `mcp_file_chunk_bm25`,
  `mcp_file_index_jobs`, `mcp_file_versions` adds an explicit `system_owner = $X` clause.
  User-facing methods pass `''`; the new `SystemFS` handle passes its owner string.
- The four `memory_*` session-memory tools (`memory_before_turn`, `memory_after_turn`,
  `memory_run_maintenance`, `memory_list_dir_with_abstract`) wrap whatever plugin `Resolve`
  returned. They **do not** expose a `plugin` arg in v1: a session crosses many turns and
  pinning a backend per session via settings is a cleaner contract than negotiating per turn.
  Add later if requested.

### 2.8 The `SystemFS` interface

A new internal-only handle, owned by `internal/mcp/files`, that the `pageindex_plugin`
(and any future plugin needing private metadata) can hold *without going through the plugin
manager*. This is the "system namespace, not reserved prefix" decision in code form.

```go
// internal/mcp/files/system_fs.go (new)
package files

// SystemFS is a restricted FS handle bound to a single non-empty system_owner.
// It is constructed via Service.SystemNamespace(owner). It is never registered
// in the plugin manager and never reachable from MCP tools.
type SystemFS interface {
    Read   (ctx, project, path string) ([]byte, error)
    Write  (ctx, project, path string, content []byte) error    // TRUNCATE only
    Delete (ctx, project, path string) error
    List   (ctx, project, prefix string) ([]string, error)       // for diagnostics only
    // Note: no Search — system data is keyed lookups, not free-text search.
    // Note: no Rename — internal callers either Write a new key or Delete.
}

func (s *Service) SystemNamespace(owner string) SystemFS  // owner must be non-empty
```

Two usage rules:

- The string passed to `SystemNamespace` is the literal `system_owner` value used in SQL.
  An empty string returns an error — there is no way to widen a `SystemFS` back into the
  user namespace.
- A `SystemFS` instance for `owner="X"` cannot read/write rows where `system_owner != "X"`.
  This is a per-instance closure, not a runtime check the caller could bypass.

The pageindex plugin obtains a `SystemFS` once at construction:

```go
// internal/mcp/memory/plugins/pageindex/plugin.go
type Plugin struct {
    userFS  *files.Service
    sysFS   files.SystemFS   // bound to "pageindex"
    indexer *Indexer         // in-process Go indexer (§2.6.4)
    cfg     Settings
}
```

Recursion is **structurally impossible**: the plugin doesn't know about the `Manager`, and
the `SystemFS` it holds has its own SQL scoping that cannot land back in the user namespace.

## 3. Risks & open questions

### 3.1 Cost

PageIndex query is LLM-bound. A typical search over a 200-page doc is 1× structure call
(small, cached) + 1 LLM "pick ranges" + 1–3 `get_page_content` reads (in-process JSON
traversal, no LLM) + answer composition upstream. We **must** cap with
`tree_query.max_steps` and `tree_query.max_tokens` and emit per-call token billing so
operators can see the bill before it surprises them. The `atomic.Int64` budget gate
(§2.6.4.2) is shared across the goroutines spawned for candidate-doc fan-out, so
concurrent search work cannot collectively over-spend a single call's budget.

Indexing cost is one-shot: an N-page PDF is ~8–30 LLM calls per §2.6.4.1 pipeline
(typical 200-page PDF: ~25 calls). The bbolt cache (§2.6.4.3) makes any subsequent
re-index of the same bytes free, and bumping `algorithm_version` is the explicit
invalidation handle.

### 3.2 Algorithmic drift from upstream PageIndex

We own the algorithm in Go. There is no pinned upstream commit and no runtime dependency on
the upstream library. The risk inverts: when VectifyAI ships an algorithmic improvement (a
new TOC-mode heuristic, a better verification prompt), we have to port it deliberately —
no automatic-upgrade path. Mitigation: a tagged `algorithm_version` constant
(`internal/mcp/memory/plugins/pageindex/version.go`) in the cache key (§2.6.4.3) plus a
quarterly audit task that diffs upstream commits since the last port and decides each.

### 3.3 Hardening the Go implementation

Two known unknowns when implementing the algorithm directly in Go:

- **PDF text extraction parity.** Upstream's text-extraction baseline runs on a
  C-extension PDF library that we are not pulling into the build. `pdfcpu`'s pure-Go
  extractor is good for prose-heavy PDFs but lags on multi-column / table-heavy
  layouts. Mitigation: ship the configurable `pdf.text_parser` knob (§2.4), add a
  corpus-comparison row to the §7 scorecard, and treat any > 5 pp regression on the
  long-doc subset of `memory-bench-internal-v1` as a release-blocker.
- **Tokenizer parity.** `tiktoken-go/tokenizer` carries embedded BPEs; the budget enforced
  by `tree_query.max_tokens` matches OpenAI's billing tokenizer to within rounding. We
  reconcile post-call against the `usage` block returned by the Responses API, so any
  mismatch surfaces as a metric, not as a silent over-spend.

### 3.4 Rename / random-IO mismatch

`file_write(... mode=OVERWRITE, offset=N)` on an indexed PDF makes no sense — overwriting page
6 of a binary PDF should not reindex page 12. v1 returns `INVALID_ARGUMENT` for non-`TRUNCATE`
writes on `.pdf`/`.md` paths under `pageindex_plugin`; the error message points to `file_delete`
+ `file_write` instead. `Capabilities.SupportsRandomIO` is **per-path** in practice but reported
as `true` because raw text paths still allow it.

### 3.5 Single shared `*files.Service` is a hot dependency

Both plugins use the same `*files.Service` instance (rag wraps it; pageindex composes it).
This concentrates load and means any latency regression in `*files.Service` hits both
plugins. Mitigations:

- The shared service is what runs today, so this is not a *new* hot dependency — only the
  observation that the refactor doesn't dilute it.
- Per-plugin metrics counters (`mcp.memory.<plugin>.*`) let us attribute slow ops correctly.
- E05 in §5.3 pins p99 latency on `rag_plugin` to ±5% of the pre-refactor baseline.

### 3.6 LLM provider lock-in (deliberate, scoped)

v1 is **deliberately scoped** to the OpenAI Responses API contract (§2.6.5). Indexing and
retrieval prompts are tuned for `gpt-5.4-mini` and not validated against any other model
in CI. This is a tradeoff:

- **What we lose:** seamless drop-in of Anthropic, Gemini, or chat-completions-only
  vendors. Any of those would require a second `LLM` implementation and re-tuning the
  PageIndex prompts (some upstream prompts contain GPT-isms that other models render less
  reliably).
- **What we gain:** zero abstraction tax. There is no `litellm`-equivalent router, no
  reflection wrapper, no "lowest common denominator" feature set. The Responses API
  ships strict JSON-Schema, server-side conversation state, and streaming in one
  endpoint — features a multi-provider abstraction has to either expose unevenly or
  hide entirely.
- **Escape hatch:** vendors that re-implement the Responses API (its surface is narrow
  enough that this is becoming common) work via `base_url` only. A different model
  contract is not available in v1; revisit if a real demand surfaces.

### 3.7 `mcp_files.system_owner` migration on a populated cluster

Adding a non-null column with a default to a hot table is fine in Postgres, but a CI gate
must enforce that no query against `mcp_files` (and friends) lacks an explicit
`system_owner = $X` predicate after the migration. The gate is an `ast-grep --lang go` rule
(per the local-search-tool preference) that fails CI if a `Query`/`QueryRow`/`Exec` against
the `mcp_*` tables doesn't include `system_owner` in its WHERE clause. False positives are
suppressed by an inline `// system_owner-checked: <reason>` comment.

## 4. Change list (file-by-file)

### 4.1 New files

| Path                                                              | Purpose                                                                              |
| ----------------------------------------------------------------- | ------------------------------------------------------------------------------------ |
| `internal/mcp/memory/plugin/plugin.go`                            | `Plugin` interface + `Capabilities`                                                  |
| `internal/mcp/memory/plugin/types.go`                             | Result value types (moved from `files/types.go`)                                     |
| `internal/mcp/memory/plugin/manager.go`                           | `Manager`, resolution rules                                                          |
| `internal/mcp/memory/plugin/manager_test.go`                      | Resolution + lifecycle tests                                                         |
| `internal/mcp/memory/plugin/settings.go`                          | `MemoryPluginSettings` + `LoadFromConfig`                                            |
| `internal/mcp/memory/plugins/rag/plugin.go`                       | Adapter over `*files.Service`                                                        |
| `internal/mcp/memory/plugins/rag/plugin_test.go`                  | Conformance suite invocation                                                         |
| `internal/mcp/memory/plugins/pageindex/plugin.go`                 | Plugin entrypoint; holds `userFS`, `SystemFS`, `*Indexer`                            |
| `internal/mcp/memory/plugins/pageindex/version.go`                | `algorithm_version` constant used as a cache-key salt (§3.2)                         |
| `internal/mcp/memory/plugins/pageindex/sysstore.go`               | `SystemFS`-based reads/writes for `pageindex/{<doc_id>.json, _meta.json, index.json}`|
| `internal/mcp/memory/plugins/pageindex/search_loop.go`            | Tree-reasoning search (§2.6.2); calls `LLM` from `llm.go`                            |
| `internal/mcp/memory/plugins/pageindex/indexer.go`                | `Indexer` orchestrator (§2.6.4); `errgroup` + `semaphore.Weighted` fan-out           |
| `internal/mcp/memory/plugins/pageindex/pipeline_pdf.go`           | Pure-Go PDF indexing pipeline (TOC detect, 3-mode dispatch, tree assembly)           |
| `internal/mcp/memory/plugins/pageindex/pipeline_markdown.go`      | Goldmark-based MD pipeline (header tree, optional summarization)                     |
| `internal/mcp/memory/plugins/pageindex/pdf_parser.go`             | `PDFParser` interface + pdfcpu/dslipak implementations (§2.6.4.5)                    |
| `internal/mcp/memory/plugins/pageindex/markdown_parser.go`        | `MarkdownParser` over `yuin/goldmark`                                                |
| `internal/mcp/memory/plugins/pageindex/llm.go`                    | `LLM` interface + `openaiLLM` (Responses API only); `tiktoken-go/tokenizer` counter  |
| `internal/mcp/memory/plugins/pageindex/prompts.go`                | The 13 prompt templates ported from upstream PageIndex (§2.6.4.1 phase table)        |
| `internal/mcp/memory/plugins/pageindex/cache.go`                  | `bbolt`-backed content-addressable cache for LLM responses (§2.6.4.3)                |
| `internal/mcp/memory/plugins/pageindex/budget.go`                 | `atomic.Int64` token-budget gate shared across goroutines                            |
| `internal/mcp/memory/plugins/pageindex/progress.go`               | `Progress` event type + MCP `report_progress` adapter                                |
| `internal/mcp/memory/plugins/pageindex/{...}_test.go`             | Unit + golden tests (canned tree, byte-offset synthesis, budget exhaustion, retry)   |
| `internal/mcp/memory/plugins/pageindex/testdata/`                 | Frozen sample PDFs/MD + expected tree JSONs for golden tests                         |
| `internal/mcp/memory/conformance/suite.go`                        | Shared "every plugin must pass" test suite                                           |
| `internal/mcp/files/system_fs.go`                                 | `SystemFS` interface + `*Service.SystemNamespace(owner)` constructor (§2.8)          |
| `internal/mcp/files/system_fs_test.go`                            | Isolation tests: SystemFS cannot read/write user namespace; user FS cannot see system rows |
| `docs/manual/mcp_memory_plugins.md`                               | Operator-facing manual                                                               |
| `docs/requirements/mcp_memory_plugins.md`                         | PRD                                                                                  |

### 4.2 Modified files

| Path                                                              | Change                                                                                                                                                                                                                                                                                                                          |
| ----------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `internal/mcp/tools/file_service.go`                              | `FileService` becomes a type alias for `plugin.Plugin`; deprecated, kept for src-compat                                                                                                                                                                                                                                         |
| `internal/mcp/tools/file_*.go`                                    | Constructors take `*plugin.Manager`; `Definition()` adds optional `plugin` input field (§2.4.1); `Handle()` decodes `req.Plugin` and forwards to `Manager.Resolve`                                                                                                                                                              |
| `internal/mcp/tools/file_tool_helpers.go`                         | New shared helper `parsePluginOverride(raw string) (string, error)` validating `""`/`"auto"`/known names                                                                                                                                                                                                                        |
| `internal/mcp/server.go` (lines 204-452)                          | Pass `*plugin.Manager` instead of `FileService`                                                                                                                                                                                                                                                                                 |
| `cmd/api.go` (lines 408-447)                                      | Build a single shared `*files.Service`; pass it to both plugins (rag wraps directly, pageindex receives `userFS` + `SystemFS("pageindex")`); build & start `plugin.Manager`                                                                                                                                                     |
| `internal/mcp/files/settings.go`                                  | Add deprecation shim reading old `settings.mcp.files.*` and forwarding into `settings.mcp.memory.plugins.rag.*`                                                                                                                                                                                                                 |
| `internal/mcp/files/service.go`, `service_*.go`, `service_search.go`, `index_worker.go`, `service_versions.go`, `lock.go` | Add `system_owner string` parameter (or context value) on every method that touches `mcp_files`/`mcp_file_chunks`/`mcp_file_index_jobs`/`mcp_file_chunk_embeddings`/`mcp_file_chunk_bm25`/`mcp_file_versions`. User-facing entrypoints pass `''`. SQL gains explicit `system_owner = $X` predicates everywhere |
| `internal/mcp/files/migration.go`                                 | Add `system_owner TEXT NOT NULL DEFAULT ''` column to all six tables; add `(system_owner, api_key_hash, project, path)` indexes; backfill is the default (no-op for existing rows)                                                                                                                                              |
| `internal/mcp/files/service_write_delete.go`                      | New `WriteOpts{ SkipRAGIndex bool }` parameter; index-job enqueue is gated on `!SkipRAGIndex`. Default behavior unchanged                                                                                                                                                                                                       |
| `internal/mcp/files/types.go`                                     | Re-export from `plugin/types.go`; add `WriteOpts`                                                                                                                                                                                                                                                                               |
| `internal/mcp/memory/service.go`                                  | Take `FileService` (interface) instead of `*files.Service`                                                                                                                                                                                                                                                                      |
| `internal/mcp/memory/engine_factory.go`                           | Read plugin name on session start; record in session metadata for observability                                                                                                                                                                                                                                                 |
| `go.mod` / `go.sum`                                               | Add `github.com/openai/openai-go`, `github.com/pdfcpu/pdfcpu`, `github.com/dslipak/pdf` (fallback), `github.com/yuin/goldmark`, `github.com/tiktoken-go/tokenizer`, `go.etcd.io/bbolt`, `github.com/avast/retry-go/v4`, `golang.org/x/sync/{errgroup,semaphore}`, `golang.org/x/time/rate`. No Anthropic SDK in v1 (§3.6). |
| `.github/workflows/*` (CI)                                        | Add the `ast-grep` rule (§3.7) that fails on missing `system_owner` predicates                                                                                                                                                                                                                                                  |
| `arch.md`                                                         | Add "MCP Memory Plugins" section pointing to manual                                                                                                                                                                                                                                                                             |

### 4.3 Deleted files

None in v1.

### 4.4 Schema migrations

Both run unconditionally on `*files.Service` startup (regardless of which plugins are enabled),
because the `system_owner` column is a property of the shared FS, not of any one plugin:

```sql
-- mcp_files, mcp_file_chunks, mcp_file_chunk_embeddings, mcp_file_chunk_bm25,
-- mcp_file_index_jobs, mcp_file_versions:
ALTER TABLE <table>
  ADD COLUMN system_owner TEXT NOT NULL DEFAULT '';
CREATE INDEX <table>_system_owner_idx
  ON <table> (system_owner, api_key_hash, project, path);
-- Existing rows default to '' (= user namespace). No data migration required.
```

`mcp_files.skip_rag_index BOOL NOT NULL DEFAULT FALSE` is added in the same migration step
(supports the `WriteOpts.SkipRAGIndex` knob from §2.6.1).

**No `mcp_pageindex_docs` table.** All PageIndex catalog/state lives in the system namespace
of `mcp_files` (rows where `system_owner='pageindex'`).

### 4.5 Settings migrations

- One-shot translation `settings.mcp.files.*` → `settings.mcp.memory.plugins.rag.*` at config
  load time, with a single WARN log per process. Removed in the release **after** v1 ships.


### 4.6 Eval harness assets and baseline_v1 deliverables

§7 introduces a quantitative scorecard that requires runnable code, golden datasets, and a
captured `baseline_v1` artifact. **None of this exists in the repository today.** All of
it lands in **Phase 1 alongside `rag_plugin`** so that the baseline can be captured before
any work on `pageindex_plugin` begins. Without these items §7 is aspirational; with them
it is reproducible by anyone who can clone the repo.

The eval harness inherits the §0 pure-Go constraint: **no external runtime, no `pip`,
no `requirements.txt`, no subprocess invocation, no extra container image.** The
RAGAS v0.4 metrics are LLM-judge prompts; we reimplement them in Go and drive the
judge through the same `LLM` Responses-API interface that production uses
(`pageindex/llm.go`). The numbers stay comparable to upstream RAGAS because the
prompts are prompt-faithful ports of the upstream templates; we cite the prompt
provenance in `ragas.go`'s package comment so a reviewer can diff against the upstream
`ragas==0.4.x` source.

#### 4.6.1 Eval harness (pure Go)

| Path                                                              | Purpose                                                                                                                              |
| ----------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------ |
| `internal/mcp/memory/conformance/eval/runner.go`                  | Top-level orchestrator. Invokes retrieval / RAGAS / red-team / ops probes against a `Plugin`; emits scorecard markdown + raw per-query JSON. |
| `internal/mcp/memory/conformance/eval/retrieval.go`               | Computes Recall@k, nDCG@10, MRR, Hit@5 against `*.jsonl` golden sets (BEIR/PyTerrier formulas).                                       |
| `internal/mcp/memory/conformance/eval/ragas.go`                   | Pure-Go reimplementation of the six RAGAS v0.4 metric prompts (`faithfulness`, `context_precision`, `context_recall`, `context_entities_recall`, `answer_relevancy`, `answer_correctness`). Uses the same `pageindex/llm.go` `LLM` interface as production for judge calls; uses `internal/mcp/rag/openai_embedder.go` for the cosine-based `answer_relevancy`. Prompt strings are ported from the upstream `ragas==0.4.x` source and cited line-for-line in the package comment. |
| `internal/mcp/memory/conformance/eval/redteam.go`                 | Runs the 12-attack OWASP injection suite plus cross-tenant, supersession, and GDPR-delete probes.                                    |
| `internal/mcp/memory/conformance/eval/ops_probe.go`               | Replay-based latency p50/p95/p99, token counters, cold-vs-warm differential, ingestion throughput probe.                              |
| `internal/mcp/memory/conformance/eval/permutation.go`             | Paired two-sided permutation test, B = 10 000 (Smucker / Allan / Carterette CIKM '07).                                                |
| `internal/mcp/memory/conformance/eval/judge_ensemble.go`          | LLM-as-judge ensemble (`gpt-4o`, `gpt-4o-mini`, `claude-opus-4-7` — all behind the same Responses-API client) with majority-true for FinanceBench answer grading. |
| `internal/mcp/memory/conformance/eval/scorecard.go`               | Markdown writer for the §7.4 scorecard template; deterministic key ordering for stable diffs.                                          |
| `internal/mcp/memory/conformance/eval/{retrieval,ragas,permutation,scorecard}_test.go` | Deterministic unit tests (judge calls served by an in-memory fake `LLM`).                                            |
| `internal/mcp/memory/conformance/eval/testdata/ragas_prompts/`    | Golden copies of the upstream RAGAS prompts at the pinned commit, used by `ragas.go`'s tests to detect prompt drift.                  |
| `cmd/eval-plugin/main.go`                                         | Driver binary that `make eval-plugin` shells into. Parses flags, constructs the `Plugin` via the production DI graph, runs `runner.go`. |

#### 4.6.2 Golden datasets (git-LFS)

| Path                                                   | Composition                                                                                       | Source                                                                  |
| ------------------------------------------------------ | ------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------- |
| `tests/eval/golden/memory-bench-internal-v1.jsonl`     | 500 hand-labelled `(query, gold_doc_set, gold_span?)` tuples (100 carry page-range gold spans).   | Sanitized export of `users.md` / `arch.md` / `tasks/*.md` / `/books`. Labelled by Laisky + 1 reviewer; Cohen's κ ≥ 0.85 before freeze. |
| `tests/eval/golden/memory-bench-internal-v1/corpus/`   | 200 documents (~150 short notes/code, ~50 long PDFs/markdown books).                              | Sanitized export.                                                         |
| `tests/eval/golden/memory-bench-ragas-v1.jsonl`        | 300 `(question, reference_answer, reference_context)` tuples (200 synthetic + 100 harvested).     | RAGAS testset generator + production logs.                               |
| `tests/eval/golden/financebench-150.jsonl`             | 150 Q's + SEC 10-K/10-Q PDFs.                                                                      | Vendored from `VectifyAI/Mafin2.5-FinanceBench` at a pinned commit.       |
| `tests/eval/golden/longmemeval_s.jsonl`                | 500 Q's, ~115 K-token contexts, 7 sub-categories.                                                  | `xiaowu0162/LongMemEval` at a pinned commit.                              |
| `tests/eval/golden/beam-1m-200.jsonl`                  | 200 category-balanced Q's.                                                                         | `mem0ai/memory-benchmarks` BEAM-1M, subsetted.                            |
| `tests/eval/golden/redteam_prompt_injection_v1.jsonl`  | 12 attacks; tenant-exfiltration variants prioritized.                                              | OWASP GenAI Data Security 2026 v1.0 catalogue.                            |
| `tests/eval/golden/README.md`                          | Provenance, labelling protocol, refresh cadence.                                                   | —                                                                        |

All large files tracked via git-LFS (see §4.6.3 `.gitattributes`).

#### 4.6.3 Build / CI wiring

| Path                                | Change                                                                                                                                                  |
| ----------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `Makefile`                          | New target `eval-plugin`: `go run ./cmd/eval-plugin --plugin=$(PLUGIN) --golden=tests/eval/golden --out=docs/eval/runs/$(shell git rev-parse --short HEAD)`. Also new `eval-baseline-rag` that runs the plugin against the frozen golden set and writes to `docs/eval/baseline_v1/`. Both targets are pure `go run`; CI runners need only the Go toolchain that the rest of the repo already requires. |
| `.github/workflows/eval-nightly.yml` | New workflow: matrix over `{rag, pageindex}`. Runs nightly + on-PR when `internal/mcp/memory/plugins/<plugin>/**` or `tests/eval/golden/**` changes. Posts the scorecard diff as a PR comment; fails the check on any §7.5 hard-gate regression. |
| `.gitattributes`                    | `tests/eval/golden/** filter=lfs diff=lfs merge=lfs -text`                                                                                              |

#### 4.6.4 Captured artifacts (checked-in, not regenerated by CI)

| Path                                              | Contents                                                                                                                                                   |
| ------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `docs/eval/README.md`                             | How to run `make eval-plugin`; how a baseline is captured / reset; who owns the harness.                                                                   |
| `docs/eval/plugin_scorecard.md`                   | Index page linking to every per-plugin scorecard; updated as plugins ship.                                                                                  |
| `docs/eval/baseline_v1/rag_plugin_scorecard.md`   | **The frozen Phase-1 baseline.** Produced by Laisky + 1 reviewer running `make eval-baseline-rag` against the Phase-1-tagged commit. Committed once and **never overwritten** until a CHANGELOG-documented baseline reset. |
| `docs/eval/baseline_v1/raw_per_query.jsonl`       | Raw per-query metric values backing the baseline. The input the permutation test reads when a future plugin is compared against `baseline_v1`.              |
| `docs/eval/baseline_v1/run_metadata.yml`          | Git SHA, run UTC, judge models, hardware spec (CPU model, RAM, container limits), Go toolchain version, embedding model + version, RAGAS-prompt commit pin. Required for replay. |
| `docs/eval/runs/<sha>/`                           | Per-run scorecards produced by CI; rolled into PR comments. Cleaned up by a retention job after 90 days.                                                    |

#### 4.6.5 Why this lives in `internal/mcp/memory/conformance/eval/`, not a top-level `tools/` directory

The harness composes against the same `plugin.Plugin` interface that production uses. By
sitting next to the conformance suite (§5.1) it shares fixtures and tenant-isolation
helpers, and there is exactly one entry point any plugin author has to look at. A future
plugin author copies an existing plugin's `make eval-plugin PLUGIN=<their_name>`
invocation and the suite runs unchanged.

## 5. Test matrix

### 5.0 Authoring principle: user-behavior-first acceptance

Acceptance criteria in this proposal describe **what an agent or operator can observe**
when interacting with the system, not **how the implementation produces that observation**.
This rule is load-bearing for two reasons:

1. **Acceptance survives refactors.** A criterion that names a Go struct field, SQL
   column, mock, or private method forces a re-write whenever the implementation
   reorganizes — even if user-visible behavior is unchanged. That is the wrong thing to
   gate a release on.
2. **Acceptance survives plugin substitution.** The whole point of this proposal is
   future plugins. A criterion that says *"pgvector is queried"* cannot be reused to
   accept a vectorless plugin; a criterion that says *"search returns the relevant chunk
   within budget"* can.

Operationally:

- **Shape.** Every criterion takes the form *"When an agent / operator does X under
  conditions Y, the system produces observable outcome Z."* Z is bytes returned, errors
  raised, latencies observed, costs billed, contents (or absence of contents) visible to
  that user in subsequent calls. Z is **not** table contents, log-line counts, mock-call
  counts, or the presence of internal cache entries.
- **Actors are named.** Multi-actor scenarios (concurrency, cross-tenant) name each
  agent/operator and each tool call explicitly.
- **Internal tests are evidence, not contract.** Mock-based unit tests, sqlmock
  assertions, lint gates, and similar live in §5.5, labelled "internal regression tests
  — informational, not acceptance." A green internal test does not, by itself, satisfy
  any A* / Q* / C* / R* criterion. Conversely, if an internal test fails but the
  user-observable outcome is correct (e.g., an internal log key was renamed), the
  internal test is what's bugged.
- **Engine differences are user-observable, or they don't appear.** Where rag and
  pageindex legitimately differ (e.g., async vs. sync indexing freshness window), the
  difference shows up as a documented user-facing parameter (a freshness window in
  seconds), not as an implementation note in the test description.

The rest of §5 is structured under this rule.

### 5.1 Conformance suite — user-observable scenarios

Every plugin runs the same parameterized suite (`internal/mcp/memory/conformance/suite.go`).
Each row describes a scenario in user terms; the right-most cell records which plugins
are expected to pass and any **user-observable** difference (never an implementation
detail).

| ID  | User scenario                                                                                                            | Expected user-observable outcome                                                                                                                       | rag | pageindex |
| --- | ------------------------------------------------------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------ | --- | --------- |
| C01 | Agent writes UTF-8 text at path P, then reads it back                                                                    | Read returns identical bytes; partial reads at offset/length return the corresponding slice                                                            | ✅  | ✅        |
| C02 | Agent writes a binary PDF at path P, then reads the full file back                                                       | Read returns identical bytes (byte-for-byte equality)                                                                                                  | ✅  | ✅        |
| C03 | Agent writes path P, then lists the parent directory                                                                     | List response contains an entry for P with the size of what was written                                                                                | ✅  | ✅        |
| C04 | Agent writes path P, then stats it                                                                                       | Stat returns `exists=true`, the byte size, and timestamps consistent with the write                                                                    | ✅  | ✅        |
| C05 | Agent writes path P, waits the engine's published freshness window, then searches for content unique to P                | Search response contains at least one chunk whose payload comes from P. Freshness window: rag ≤ 5 s; pageindex returned in-line with the write call    | ✅  | ✅        |
| C06 | Agent writes P, then deletes it, then lists                                                                               | List response no longer contains an entry for P                                                                                                        | ✅  | ✅        |
| C07 | Agent writes P, renames P → Q, then reads each                                                                           | Read on Q returns the original bytes; read on P returns NOT_FOUND                                                                                      | ✅  | ✅        |
| C08 | Agent writes P (TRUNCATE), then writes P again (TRUNCATE) with different content                                          | Subsequent read returns the second write's bytes only                                                                                                  | ✅  | ✅        |
| C09 | Agent writes path P at offset N (OVERWRITE) on a text path                                                               | Read returns bytes spliced exactly at offset N                                                                                                         | ✅  | ✅        |
| C09b | Agent attempts OVERWRITE@offset on a `.pdf` or `.md` path                                                                | Tool returns `INVALID_ARGUMENT` with a message naming the alternative (`file_delete` + `file_write`)                                                   | n/a | ✅        |
| C10 | Two agents concurrently write the same path P (TRUNCATE) with different payloads X and Y                                 | Both calls return success **or** at most one returns CONFLICT; subsequent read returns exactly X **or** exactly Y. Never a mix; never empty; never an error after a "success" was returned | ✅ | ✅ |
| C11 | Two agents concurrently write **different** paths in the same project                                                     | Both succeed; subsequent reads of each path return its own bytes; neither write is delayed by the other beyond the single-call latency budget          | ✅  | ✅        |
| C12 | Tenant A writes content at project X. Tenant B (different API key) issues `file_search` / `file_list` / `file_read` against project X, including with `path_prefix="*"` | None of B's calls return any of A's content (path names, chunk text, sizes, error codes that depend on A's content)                                       | ✅ | ✅ |
| C13 | Tenant A writes content; tenant B tries to read by guessing A's exact path                                                | Read returns NOT_FOUND. The error response carries no signal whose value depends on whether the path exists in A's namespace (no oracle)               | ✅  | ✅        |
| C14 | Agent writes `/a/x`, `/a/y`, `/b/z`, then searches with `path_prefix="/a"`                                                | Search results never include content from `/b/z`                                                                                                        | ✅  | ✅        |
| C15 | Agent searches with `limit=N`                                                                                              | Response contains at most N entries                                                                                                                     | ✅  | ✅        |
| C16 | Agent searches with `project="*"` from a single API key that owns content in two projects                                  | Response includes results from both projects, each tagged with its own project name; no content from other tenants                                      | ✅  | ✅        |
| C17 | Agent calls each tool with empty path, empty project, or path containing `..`                                              | Tool returns `INVALID_ARGUMENT` with a clear message; never silently writes outside the tenant root                                                    | ✅  | ✅        |
| C18 | Agent writes a long document (rag: ≫ chunk threshold; pageindex: ≥ 100-page PDF), then searches for content known to live in the last quarter of the document | At least one returned chunk overlaps the last quarter of the document; result quality meets the §7 thresholds                                          | ✅ | ✅ |
| C19 | Agent writes arbitrary bytes (including non-UTF-8) at P, then reads back                                                   | Read returns the same bytes                                                                                                                             | ✅  | ✅        |
| C20 | Agent writes P, deletes it, then reads, stats, and searches for its content                                                | Read and stat return NOT_FOUND; search responses contain no chunk derived from P                                                                        | ✅  | ✅        |
| C21 | Agent calls a tool **without** the `plugin` field                                                                          | Behavior is identical to `plugin="auto"`; subsequent reads see the write                                                                                | ✅  | ✅        |
| C22 | Agent calls a tool with `plugin="auto"`                                                                                    | Identical to C21                                                                                                                                        | ✅  | ✅        |
| C23 | Project default is pageindex. Agent writes path P with `plugin="rag"`, then reads with `plugin="rag"`, then with `plugin="pageindex"` | First read returns the bytes; second read returns NOT_FOUND with a hint pointing to `plugin="rag"`                                                       | ✅ | n/a |
| C24 | Mirror of C23 (default rag; agent uses `plugin="pageindex"`)                                                                | As C23                                                                                                                                                  | n/a | ✅        |
| C25 | Agent calls a tool with `plugin="bogus"`                                                                                   | `INVALID_ARGUMENT`; response includes the list of valid plugin names                                                                                    | ✅  | ✅        |
| C26 | Agent writes P with `plugin="A"`, then reads P with `plugin="B"`                                                            | Read returns NOT_FOUND with a hint identifying the plugin that owns P (no silent cross-plugin fallback)                                                | ✅  | ✅        |
| C27 | A user has no way to create the system-owned routing/catalog state a plugin uses internally. Trying to do so via standard tool calls (e.g., writing to a path that *would* collide with internal state) | The user's write succeeds and is visible to that user only; system-owned state is unchanged and never appears in that user's `file_list` / `file_search` / `file_read` responses | ✅ | ✅ |

### 5.2 Behavioral race-condition matrix

Race conditions are user-observable: two agents do something at the same time and the
user expects a deterministic outcome. Each row pairs an action sequence with the **only**
acceptable observations. No row asserts which lock primitive is used.

| ID  | Concurrent actions                                                                                  | Acceptable user-observable outcomes                                                                                                                                                                                                                                                                                          |
| --- | --------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| R01 | Agent A writes P with bytes X; Agent B writes P with bytes Y (TRUNCATE; calls overlap in time)      | Both calls return success **or** at most one returns CONFLICT. A subsequent read returns exactly X or exactly Y. Never: partial bytes mixed from both; never empty; never an error after a success was returned to the agent.                                                                                                |
| R02 | A writes P; B deletes P (calls overlap)                                                              | One of three deterministic outcomes per a documented order rule: (i) write→success then read returns bytes; (ii) write→success, delete→success, read→NOT_FOUND; (iii) write→CONFLICT and the path is absent. The same input sequence always produces the same outcome.                                                       |
| R03 | A reads P; B writes P (TRUNCATE); calls overlap                                                      | A's read returns either pre-write bytes or post-write bytes — never a mix. A's call latency is bounded by the read budget (no head-of-line blocking by the long write).                                                                                                                                                       |
| R04 | A renames P→Q; B reads P; calls overlap                                                              | B's read returns either the original bytes (rename hadn't committed) or NOT_FOUND (rename committed). After both calls return, a subsequent list shows Q but never both P and Q.                                                                                                                                              |
| R05 | A deletes P; B reads P; calls overlap                                                                | B's read returns either the bytes or NOT_FOUND, deterministically. Any subsequent search returns no chunk that originated from P after delete returned success.                                                                                                                                                               |
| R06 | A writes P, then immediately B searches for content unique to P                                      | If the engine advertises freshness window ≤ T (rag = 5 s, pageindex = synchronous = 0 s), then for any B-search invoked at any moment ≥ T after A's write success, the search response contains a chunk derived from P. Earlier than T may legitimately miss it; T is part of the user contract documented in `Capabilities`. |
| R07 | N=20 agents write distinct paths in the same project concurrently                                     | All 20 writes succeed within the per-call latency budget; subsequent reads return each path's own bytes. The slowest of the 20 is bounded by `latency_budget × O(log N)` — no head-of-line blocking.                                                                                                                          |
| R08 | A writes P; concurrently the host process restarts (rag's index worker resumes from `mcp_file_index_jobs`; pageindex's in-process indexer is re-driven by the next write or the warm bbolt cache) | After restart, a read of P returns either the bytes or `UNAVAILABLE` (transient); never empty success. Within the freshness window, a search re-derives P at most once (no double-billing).                                                                                                                                  |
| R09 | A writes P with `plugin="rag"`; B concurrently writes the same P with `plugin="pageindex"`            | Both writes succeed. A subsequent read scoped to a plugin returns only that plugin's bytes; cross-plugin reads return NOT_FOUND with the routing hint. The two are tracked independently — neither mutates the other.                                                                                                          |
| R10 | A writes P; thousands (C ≥ 1 000) of concurrent reads of P                                            | Every read returns either pre-write or post-write bytes consistently per call; A's write returns success. Read tail latency does not exceed `latency_budget × 1.5` even at C = 1 000.                                                                                                                                          |

R01–R10 are acceptance: every plugin must satisfy them on a real database. They are run
under `go test -race -count=10` over a fault-injecting harness with explicit scheduling
points (channel rendezvous, not Go-runtime mocks). No row asserts which lock primitive is
used; rows assert what the concurrent agents observe.

### 5.3 Integration / e2e — operator scenarios

| ID  | Operator scenario                                                                                                       | Expected observation                                                                                                                                                                              |
| --- | ----------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| E01 | Operator runs the server with `default_plugin=rag` against the existing test corpus                                     | The pre-refactor user-visible behavior is preserved end-to-end: every existing functional test under `internal/mcp/files` and `internal/mcp/tools` is green; the §5.4 smoke flow works            |
| E02 | Operator runs with `default_plugin=pageindex` (Responses-API key configured), writes a 100-page PDF, then queries it      | `file_search` returns at least one chunk whose pages overlap the answer; total wall-clock for write→search ≤ §7.5 thresholds                                                                      |
| E03 | Operator configures mixed projects (`notes→rag`, `books→pageindex`) and queries each                                     | The MCP `tools/list` schemas served to clients are byte-identical to single-plugin mode; queries land on the documented plugin (verifiable via the operator's standard audit channel)             |
| E04 | Operator monitors a `pageindex` `file_search` invocation                                                                  | Per-call billing metadata is emitted (token counts in/out, LLM call count) so the operator can budget. Exact metric key names are defined in the operator manual, not pinned in this proposal     |
| E05 | Operator measures `rag` plugin hot-path latency before and after the refactor                                            | p99 latency unchanged within ±5%                                                                                                                                                                  |
| E06 | Operator writes a 200-page PDF under `pageindex`                                                                          | Operator sees a sequence of progress signals (in any documented form) before the call returns; the call returns within the configured indexing timeout                                            |
| E07 | Operator restarts the server, then immediately searches a previously-indexed PDF                                          | First search after restart succeeds within the configured query timeout; the result set is equivalent to a warm-cache search of the same query (same top-K paths, allowing rank ties)             |

### 5.4 Manual / smoke

- `go run -race main.go -c /opt/configs/laisky-blog-graphql/test/settings.yml api -t "heartbeat" --listen "0.0.0.0:17800" --debug`
- Hit MCP `tools/list` from the heartbeat client; verify the `file_*` tool schemas match the documented diff (one new optional `plugin` field; everything else byte-identical).
- Toggle `default_plugin` between `rag` and `pageindex` via env override; replay the smoke script (§5.3 E01) under each.
- Reach the dev frontend at `http://100.75.198.70:5173/` and exercise any UI panel that lists files; verify no visible change.

### 5.5 Internal regression tests (informational, not acceptance)

The tables in this section are valuable engineering evidence that A* / Q* / C* / R* are
satisfied — but they describe **how** to verify, not **what** the system promises. A green
internal test does not, on its own, satisfy any acceptance criterion. They live here so
reviewers can see how the team validates internally, without the contents of the contract
becoming fragile to internal renames.

#### 5.5.1 `rag_plugin` regression (existing tests, retained)

- Hybrid-search ranking — `service_search_test.go`
- Index-worker job claim/retry/embedding — `index_worker_test.go`
- Advisory-lock retry/timeout — `lock_test.go`, `lock_postgres_integration_test.go`
- Outbox enqueue on write/delete/rename — `service_outbox_test.go`
- Versions — `service_versions_test.go`

#### 5.5.2 `pageindex_plugin` regression

| ID  | Internal test (informational)                                                                                                                                                       |
| --- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| P01 | `Indexer.Index(KindPDF, ...)` happy path against a frozen 20-page PDF in `testdata/`. Stub `LLM` returns canned Responses-API JSON; assert tree structure matches a golden fixture byte-for-byte (modulo `indexed_at`). |
| P02 | `Indexer.Index(KindMarkdown, ...)` happy path: header tree extracted by goldmark; no LLM calls when `algo.generate_node_summary=false`. |
| P03 | `Search` loop given canned tree + stub `LLM`: asserts merge order, byte-offset synthesis, `limit` truncation.                                                                       |
| P04 | `Search` loop budget exhaustion (`max_steps=2`) → returns partial result with `truncated=true`, no error. (User-observable side covered by C-row + Q7.)                              |
| P05 | `Search` loop with malformed LLM JSON → Responses-API strict-schema rejection → `LLM.Respond` returns error → degrades to "fetch first node of each candidate" fallback. (User-observable side covered by C-row.) |
| P06 | `Write(.pdf, OVERWRITE@offset=10)` → `INVALID_ARGUMENT`. (User-observable side covered by C09b.)                                                                                     |
| P07 | `Write(.pdf, TRUNCATE)` then `Read(.pdf)` returns identical bytes via `userFS`.                                                                                                      |
| P08 | `Delete` removes user row from `userFS` and the `pageindex/<doc_id>.json` row from `SystemFS`; index mapping updated atomically.                                                     |
| P09 | `Rename` updates `userFS` row + index mapping; tree JSON content untouched.                                                                                                          |
| P10 | Index-mapping mutation is serialized: 10 concurrent writes to distinct user paths in the same project all succeed; final mapping has 10 entries.                                    |
| P11 | LLM provider unreachable: `openaiLLM` returns `429`/`5xx`/network error; `avast/retry-go/v4` retries per policy honoring `Retry-After`; after exhaustion, surfaces as a tool-level `unavailable` error. (User-observable side covered by C-rows.) |
| P12 | Concurrency cap: spinning 100 `file_write(.pdf)` calls in parallel never exceeds `indexer.max_concurrency` simultaneous LLM calls (verified by an instrumented stub `LLM`).          |
| P13 | `WriteOpts.SkipRAGIndex=true` means the index worker observes no new job for the row.                                                                                                |
| P14 | Streaming progress: `Index` writes one `Progress` event per phase to a buffered channel; events are forwarded to a captured logger and to billing metadata; the final tool response still arrives as one unit. |
| P15 | bbolt cache hit: re-running `Index` on the same bytes with the same `algorithm_version` makes zero `LLM.Respond` calls.                                                              |
| P16 | bbolt cache invalidation: bumping `algorithm_version` makes the next `Index` re-run all prompts.                                                                                     |
| P17 | Token budget enforcement: a stub `LLM` reporting outsized usage causes the shared `atomic.Int64` budget to fall below zero; subsequent goroutines short-circuit with `ErrBudgetExceeded`. |
| P18 | PDF parser swap: setting `pdf.text_parser="dslipak"` routes through the dslipak adapter; the same golden test in P01 still passes (modulo documented text-quality differences).      |

#### 5.5.3 Manager / settings regression

| ID  | Internal test (informational)                                                                                                                                  |
| --- | -------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| M01 | With no per-call `plugin` argument, every call resolves to `default_plugin`.                                                                                    |
| M02 | Unknown plugin name in config returns startup error.                                                                                                            |
| M03 | `StartAll` failure on one plugin aborts startup with a wrapped error naming the plugin.                                                                         |
| M04 | Settings shim: legacy `settings.mcp.files.*` is read into `settings.mcp.memory.plugins.rag.*`.                                                                  |
| M05 | Settings shim emits exactly one WARN per process (not per request).                                                                                             |
| M06 | `Capabilities.SupportsVersions=false` makes the versions-aware tool path fall back gracefully.                                                                  |
| M07 | Per-call `plugin` arg, when present and valid, wins over `default_plugin` for that call only — subsequent calls without the arg revert to `default_plugin`.    |
| M08 | `plugin=""` and `plugin="auto"` are equivalent; only the resolution-rule path is exercised.                                                                     |
| M09 | Resolved plugin name is recorded on every request log (asserted with a captured logger).                                                                        |
| M10 | `system_owner` lint gate: a deliberately broken commit that adds a query against shared FS tables without `system_owner` predicate fails the CI `ast-grep` check.|
| M11 | `SystemFS("")` returns an error.                                                                                                                                |
| M12 | A `SystemFS("X")` instance cannot read/write rows where `system_owner != "X"` (constructed via reflection in test only — production callers cannot reach the SQL directly). |

#### 5.5.4 Why this section exists

A previous draft of this proposal mixed these tests into the conformance suite. That made
acceptance fragile — renaming an internal Go struct field caused an "acceptance" row to
fail even when the user's observation was unchanged. Pulling these out keeps the contract
clean.

## 6. Acceptance criteria

Every criterion below follows §5.0: it is satisfied iff the **observable outcome** is
produced when an agent or operator does the named action. Tests are listed as evidence,
not as the criterion itself.

A1. **Existing user-visible behavior is preserved on `rag`-routed calls.**
   When any agent that worked against the pre-refactor system replays its full history of
   tool calls, every observable response (returned bytes, errors, list/stat results,
   search ordering at top-K, per-call latency within ±5%) is the same as before.
   *Evidence:* the existing test corpus under `internal/mcp/files/` and
   `internal/mcp/tools/` is green under `go test -race -count=1 ./internal/mcp/...`.

A2. **The public schema is additive-only.**
   When a client calls MCP `tools/list`, the response for every `file_*` tool differs
   from the pre-refactor response by exactly one new optional input field (`plugin`,
   enum `["rag","pageindex","auto"]`, default `"auto"`); every other field is
   byte-identical. Pre-existing callers that omit the field observe identical behavior to
   today.

A3. **A user can switch plugins without changing tool-call shape.**
   When an operator switches `default_plugin` between `rag` and `pageindex` and an agent
   re-runs the same script (write → list → stat → search → read → delete), every call
   returns success with payloads that satisfy the §5.1 conformance rows for the active
   plugin. The agent's code never changes between configurations.

A4. **All §5.1 conformance rows and §5.2 race-condition rows pass for every plugin.**
   When the conformance suite runs against a plugin (real DB; stub `LLM` returning canned
   Responses-API JSON in CI for `pageindex`), every C-row and every R-row produces exactly
   one of the documented user-observable outcomes.

A5. **Legacy config remains valid.**
   When an operator boots the server with a config that sets only the legacy
   `settings.mcp.files.*` keys, the server boots, prints exactly one deprecation warning
   on startup, and the user-visible behavior of all tool calls is identical to a config
   that sets the new keys equivalently.

A6. **A new operator can run `pageindex` end-to-end from the manual.**
   When an operator who has never seen the project follows the "Enable pageindex"
   section of `docs/manual/mcp_memory_plugins.md` on a 2-vCPU box (set `llm.api_key`
   in `settings.yml`, flip `default_plugin: pageindex`, restart), the §5.3 E02 scenario
   succeeds on the first try. Bring-up is config-only — the existing Go process owns
   the pipeline.

A7. **Cost is budgeted and visible to the agent.**
   When an agent issues a `file_search` under `pageindex` and the configured budgets
   (`tree_query.max_steps`, `tree_query.max_tokens`) would be exceeded, the response is
   returned **before** the budget is exceeded, contains the partial result, and includes
   `truncated=true` in `structuredContent`. Token spend per call is observable to the
   operator (the exact telemetry path lives in the operator manual).

A8. **A user never sees content that does not belong to their tenant.**
   For any pair of API keys (A, B), there is no sequence of tool calls B can issue that
   surfaces content authored by A — neither directly (`file_read` / `file_search` /
   `file_list` results), nor indirectly (path existence inferred from error codes, byte
   sizes, or response-time differences exceeding 50 ms). This includes content that the
   system itself wrote on behalf of plugins; the system's internal namespace is
   unobservable from any user-side tool call.
   *Evidence:* §5.1 C12, C13, C27; the §5.2 race-condition rows; §6.1 Q10; the
   cross-tenant red-team probe.

A9. **Operator-facing docs land with the code.**
   When the PR merges, an operator can read `docs/manual/mcp_memory_plugins.md` and the
   new section in `arch.md` to bring up, switch, and tear down each plugin without
   reading source.

A10. **Reverting to single-plugin mode produces pre-refactor behavior.**
   When an operator sets `default_plugin=rag` and removes the `pageindex` block from
   config (or simply leaves `llm.api_key` empty), every tool call's observable outcome
   matches the pre-refactor system on the same input. No migration leaves behind a
   user-visible change. With no API key configured, any explicit `plugin="pageindex"`
   call returns `FAILED_PRECONDITION` with a message naming the missing config key —
   not silent fallback.


A11. **Switching the indexing/retrieval model within the Responses-API contract is invisible to the agent.**
   When an operator changes `llm.indexing_model` and `llm.retrieve_model` to another
   model that the configured `base_url` exposes via the Responses API (e.g., a
   self-hosted vendor), `pageindex_plugin` indexing and search succeed and produce
   results that meet the §7.5 hard gates for `pageindex_plugin`. The agent's
   request/response shape never depends on the model choice. Switching to a *non*-
   Responses-API contract (chat-completions, Anthropic Messages) is explicitly out of
   scope for v1 and is documented as such; the system fails closed (startup error)
   rather than silently degrading.

A12. **Long indexing operations report progress.**
   When an operator (or agent) writes a long document under `pageindex` and the
   operation takes longer than the configured progress-emit interval, the operator
   sees a sequence of progress signals (in any documented form) before the call
   returns. On completion, the cumulative cost (token counts, LLM call count) is
   visible to the operator via the documented telemetry channel.

### 6.1 Quantitative acceptance criteria (`Q*`)

These extend §6 with the measurable bar required by §7. Every `Q*` is automatically asserted
by `make eval-plugin` and produces a row in the plugin scorecard.

Q1. **Internal retrieval quality (gold-labelled set).** On `memory-bench-internal-v1`,
    every plugin meets the §7.5 hard gates for Recall@10, nDCG@10 (overall and long-doc
    subset), MRR, and Hit@5. `pageindex_plugin` exceeds `rag_plugin` nDCG@10 on the
    long-doc subset by ≥ 10 pp with a paired permutation test p < 0.05, B = 10 000.

Q2. **End-to-end RAG quality (RAGAS v0.4).** On `memory-bench-ragas-v1`, every plugin
    meets faithfulness ≥ 0.85, context_recall ≥ 0.80, answer_correctness ≥ 0.75. Watch
    thresholds in §7.5 are enforced as PR-level review gates.

Q3. **PageIndex parity on FinanceBench-150.** `pageindex_plugin` scores ≥ 95.0% on the
    public 150-Q split using the vendored `Mafin2.5-FinanceBench` LLM-judge ensemble
    (≤ 4 pp below VectifyAI's published 98.7%, accounting for our judge ensemble being
    stricter than the original single-judge run).

Q4. **LongMemEval_S accuracy.** `pageindex_plugin` ≥ 0.65, `rag_plugin` ≥ 0.55 on the
    500-question set. Both report per-category scores (info-extraction, multi-session,
    knowledge-update, temporal, abstention).

Q5. **BEAM-1M (200-question subset).** Reported per-category. `pageindex_plugin` ≥ 0.55
    overall; `rag_plugin` ≥ 0.40 overall; both ≥ 0.50 on `abstention` (we will not ship
    a memory plugin that confidently makes things up).

Q6. **Latency.** `rag_plugin`: `file_search` p95/p99 within ±5%/±10% of the captured
    `baseline_v1`. `pageindex_plugin`: `file_search` p95 ≤ 8 000 ms, p99 ≤ 15 000 ms.

Q7. **Cost.** `pageindex_plugin`: mean tokens per `file_search` ≤ 12 000; $ per 1 000
    `file_search` calls at the configured `retrieve_model` ≤ $4.00. `rag_plugin`:
    $ per 1 000 calls ≤ $0.50 (no LLM in the hot path).

Q8. **Index throughput.** `rag_plugin` ≥ 1 500 text-pages/min/worker on the standard
    2-vCPU/8 GiB worker; `pageindex_plugin` ≥ 50 PDF-pages/min/worker.

Q9. **Adversarial — prompt-injection.** Both plugins block ≥ 11/12 attacks from the OWASP
    GenAI Data Security 2026 v1.0 catalogue (cross-tenant exfiltration variants
    prioritized).

Q10. **Adversarial — cross-tenant.** **Zero** hits across 100 random cross-tenant
    `file_search` probes; verified at chunk-fetch level, not just at response level.

Q11. **Adversarial — supersession & GDPR delete.** ≥ 48/50 contradiction pairs return
    the newer fact at query time. After `file_delete`, p95 time-to-unreachable on
    `file_search` and `file_read` ≤ 1 000 ms; within 24 h the operator-visible
    storage cost attributable to the deleted path returns to its pre-write baseline
    (no residual recall, no residual storage). The user has no way to observe a
    difference between "path was deleted" and "path was never written" beyond the
    absence of contents.

Q12. **Drift monitor.** Replaying `memory-bench-internal-v1` queries one week after the
    baseline run regresses by ≤ 2 pp absolute on nDCG@10 for any plugin; > 1 pp triggers
    a watch-level review.

## 7. Quantitative evaluation framework (2026 industrial standard)

This section turns "the new plugin works" into a measurable contract. It (a) gives every
plugin — current and future — the same scorecard so upgrades are decidable, and (b) anchors
the proposal to public 2026 benchmarks where they exist.

### 7.1 Why a separate quantitative section

The acceptance criteria in §6 prove correctness (does it crash, does it isolate tenants,
does it preserve schemas). They do **not** prove **quality**. In 2026, every memory/RAG
vendor that ships in production publishes a multi-axis scorecard rather than a single hero
number — see Mem0's research grid (LoCoMo / LongMemEval / BEAM-1M / BEAM-10M, mean
tokens-per-retrieval), Zep's LongMemEval + p95 latency table, Mastra and Honcho's
leaderboards, and Letta's read / write / update split. Our scorecard borrows the **shape**
of that consensus and instantiates it on benchmarks public enough that we (and reviewers)
can replicate, plus internal golden sets that match how our agents actually use this
surface.

A single 98.7%-style headline is explicitly rejected: PageIndex's own 98.7% on FinanceBench
is on a 150-question public split with a permissive GPT-4o LLM-judge — useful as a parity
check, not as a global quality claim.

### 7.2 The four axes

| Axis                                | What it answers                                                      | Where the numbers come from                                       |
| ----------------------------------- | -------------------------------------------------------------------- | ----------------------------------------------------------------- |
| **R**etrieval quality               | "Does `file_search` return the right chunks?"                        | Internal gold-labelled set + BEIR-style retrieval metrics         |
| **G**eneration quality (end-to-end) | "If an agent uses these chunks, are answers grounded and correct?"   | RAGAS v0.4 metrics on a synthetic + harvested QA set              |
| **L**ong-doc / memory specifics     | "Does the plugin actually beat the alternative on long PDFs / multi-session memory?" | FinanceBench-150, LongMemEval_S, BEAM-1M (subset)                 |
| **O**perational                     | "Can we afford to run it?"                                           | Internal latency / cost / throughput probes; OWASP-aligned red-team |

### 7.3 Datasets and metric definitions

#### 7.3.1 Internal retrieval golden set (`memory-bench-internal-v1`)

- **Composition:** 200 documents (~150 short notes/code, ~50 long PDFs/markdown books)
  drawn from sanitized production exports of the `users.md` / `arch.md` / `tasks/*.md` /
  `/books` shapes. 500 hand-labelled `(query, gold_doc_set, gold_span?)` tuples; 100 of
  them carry page-range gold spans for long-doc evaluation.
- **Labelling:** authors (Laisky + 1 reviewer); inter-rater agreement ≥ 0.85 Cohen's κ
  before freezing the set; relabel any tuple with disagreement.
- **Metrics:** Recall@10, nDCG@10, MRR, Hit@5 — BEIR/PyTerrier-canonical formulas;
  nDCG@10 is the headline, the others trend with it.
- **Granularity:** computed at document level **and** at chunk/page level (long-doc
  subset uses page-range overlap as relevance, per the long-document-retrieval survey
  arXiv:2509.07759).

#### 7.3.2 End-to-end generation quality (RAGAS v0.4 metrics)

- **Harness:** pure-Go reimplementation in `internal/mcp/memory/conformance/eval/ragas.go`
  (per §0). The six metric prompts are line-for-line ports of the upstream
  `ragas==0.4.x` source at a pinned commit; the reference prompts live as golden
  fixtures under `internal/mcp/memory/conformance/eval/testdata/ragas_prompts/` and a
  unit test fails on prompt drift. Judge model `gpt-4o-mini`, embedding model
  `text-embedding-3-small` — both invoked through the same Responses-API `LLM` and
  embedder clients production already uses, so numbers stay comparable to upstream
  RAGAS published results.
- **Question set (`memory-bench-ragas-v1`):** 200 synthetic + 100 production-harvested
  `(question, reference_answer, reference_context)` tuples per plugin domain.
- **Metrics tracked:** `faithfulness`, `context_precision`, `context_recall`,
  `answer_correctness`, `answer_relevancy`, `context_entities_recall`. Faithfulness and
  context_recall are the **gating** ones; the rest are reported.

#### 7.3.3 Public benchmarks (parity + differentiation)

- **FinanceBench-150** (Patronus AI open split, 150 Q's, SEC 10-K / 10-Q PDFs).
  - Why: it is the only quantitative benchmark VectifyAI publishes on PageIndex
    (Mafin2.5 = 98.7% accuracy at 100% coverage, GPT-4o single-judge). Running it in
    our own harness gives an apples-to-apples sanity check that our integration of the
    library is not lossy.
  - Harness: vendored from `VectifyAI/Mafin2.5-FinanceBench`; LLM-judge **ensemble**
    (`gpt-4o`, `gpt-4o-mini`, `claude-opus-4-7`) with majority-true to dampen
    single-judge bias and inflated permissive matching.
  - Both plugins are run on the same documents so the gap is observable.
- **LongMemEval_S** (Wu et al., ICLR 2025; 500 Q's, ~115 K-token contexts, 7
  sub-categories: info-extraction, single-session-user/assistant/preference,
  knowledge-update, multi-session, temporal-reasoning, abstention).
  - Why: the de-facto leaderboard for memory systems in 2026 (Mem0, Zep, Mastra, Honcho
    all publish on it).
  - Used to bench `pageindex_plugin` vs. `rag_plugin` on the **search** surface only;
    the full session-memory stack (`memory_*` tools) is out of scope for v1, but the
    question set still fits the file_io abstraction.
- **BEAM-1M** (Mem0 community harness, 700 Q's; 200-Q category-balanced slice for CI:
  preference, instruction-following, contradiction-resolution, event-ordering,
  abstention, summarization).
  - Why: gives us the contradiction / abstention / preference axes that LongMemEval and
    LoCoMo do not cover well.
- **(Optional, deferred)** LoCoMo, RULER, BABILong, NoLiMa, ∞Bench. Reported in the
  scorecard if a reviewer asks; not gating.

#### 7.3.4 Operational probes

| Probe                                 | Method                                                                       | Where reported                  |
| ------------------------------------- | ---------------------------------------------------------------------------- | ------------------------------- |
| `file_search` latency p50 / p95 / p99 | 2 000 replays of the harvested production query set against a warm process   | Grafana board + scorecard line  |
| `file_write` index-completion latency | Time from `Write` ack to "searchable" (per-plugin synchronous vs. async)     | Grafana                          |
| Tokens in/out per `file_search`       | Mean and p95 over the same 2 000-replay run                                  | Scorecard, billed per call      |
| $ per 1 000 `file_search` calls       | Tokens × current model price (config-driven)                                 | Scorecard                        |
| Index ingestion throughput            | Docs/hour and pages/hour on a 2-vCPU 8 GiB worker, 100-PDF corpus            | Scorecard                        |
| Cold p95 vs. warm p95 differential    | First call after restart vs. 100th call in steady state                      | Scorecard                        |

#### 7.3.5 Adversarial / red-team probes

| Probe                                                                   | Pass criterion                                                                                                  |
| ----------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------- |
| Prompt-injection suite (12 attacks, OWASP GenAI 2026 v1.0 catalogue)    | Plugin must not exfiltrate cross-tenant content, must not return system-prompt content; ≥ 11/12 blocked         |
| Cross-tenant retrieval probe                                            | 100 queries from tenant A targeting tenant B's known content via `path_prefix=*`; **0 hits** allowed             |
| Contradiction / supersession                                            | 50 pairs `(write F1 at t1, write F2 at t2 contradicting F1)`; query at t3 must surface F2, not F1; ≥ 48/50 pass |
| GDPR-delete recall                                                      | After `file_delete`, the path must be unreachable via `file_read` *and* `file_search` within 1 s p95            |
| Embedding / index drift                                                 | Re-running last week's golden queries today must not regress > 2 pp absolute nDCG@10                            |

### 7.4 The plugin scorecard (template)

Every plugin (current and future) publishes one row of this table, regenerated by
`make eval-plugin PLUGIN=<name>` on every PR that touches the plugin. The scorecard lives
under `docs/eval/plugin_scorecard.md` and is part of the doc-landed acceptance (A9).

```
plugin: <name>                           run_id: <git_sha>:<utc_iso>
golden_set:        memory-bench-internal-v1
ragas_set:         memory-bench-ragas-v1
public_set_a:      financebench-150
public_set_b:      longmemeval_s
public_set_c:      beam-1m-200

[Retrieval quality — internal]
recall@10              <r>
ndcg@10                <n>
mrr                    <m>
hit@5                  <h>
ndcg@10 (long-doc)     <n_ld>

[Generation quality — RAGAS v0.4]
faithfulness           <f>
context_recall         <cr>
context_precision      <cp>
answer_correctness     <ac>
answer_relevancy       <ar>
context_entities_recall <cer>

[Public benchmarks]
financebench-150                  <%>
longmemeval_s (overall + 7 cats)  <%> [...]
beam-1m (200, 6 cats)             <%> [...]

[Operational]
file_search p50/p95/p99 (ms)         <a>/<b>/<c>
file_write→searchable p95 (ms)        <d>
tokens_in/out per search (mean,p95)   <e>,<f> / <g>,<h>
$ per 1K searches @ <model>           <i>
index throughput (pages/min/worker)   <j>
cold-p95 - warm-p95 (ms)              <k>

[Adversarial]
prompt_injection blocked       <≥11>/12
cross_tenant_hits              0
supersession_correct           <≥48>/50
gdpr_delete_recall_ms_p95      <≤1000>
weekly_drift_ndcg10            <≤2.0pp>
```

### 7.5 Gating thresholds

Numbers are calibrated to 2026 published practice (premai shipping gate
`faithfulness ≥ 0.85`; LongMemEval typical commercial-assistant range 60–95%) and to
each engine's design strengths. Two threshold classes:

- **Hard gate** — blocks merge / phase promotion.
- **Watch** — non-gating, but a regression beyond the listed delta triggers a PR review.

| Metric                                | `rag_plugin` hard gate              | `pageindex_plugin` hard gate                                | Watch       |
| ------------------------------------- | ----------------------------------- | ----------------------------------------------------------- | ----------- |
| Internal Recall@10 (overall)          | ≥ baseline_v1 − 2 pp                | ≥ baseline_v1 − 5 pp on short corpus                        | any drop    |
| Internal nDCG@10 (long-doc subset)    | ≥ baseline_v1                       | **≥ baseline_v1 + 10 pp** (this is the *point* of pageindex) | —          |
| RAGAS faithfulness                    | ≥ 0.85                              | ≥ 0.85                                                      | < 0.90      |
| RAGAS context_recall                  | ≥ 0.80                              | ≥ 0.80                                                      | < 0.85      |
| RAGAS answer_correctness              | ≥ 0.75                              | ≥ 0.75                                                      | < 0.80      |
| FinanceBench-150 accuracy             | ≥ 0.50 (acknowledged weak corpus)   | ≥ 0.95 (parity with VectifyAI − 4 pp)                       | —           |
| LongMemEval_S accuracy                | ≥ 0.55                              | ≥ 0.65                                                      | < 0.70      |
| BEAM-1M (200 subset)                  | ≥ 0.40                              | ≥ 0.55                                                      | —           |
| `file_search` p95 latency             | ≤ baseline_v1 × 1.05                | ≤ 8 000 ms                                                  | > 5 000 ms  |
| `file_search` p99 latency             | ≤ baseline_v1 × 1.10                | ≤ 15 000 ms                                                 | —           |
| `file_write→searchable` p95           | ≤ 30 s (async)                      | ≤ 90 s (sync index)                                         | —           |
| Mean tokens / `file_search`           | n/a (not LLM-bound)                 | ≤ 12 000 (Mem0-range upper)                                 | > 20 000    |
| $ per 1 000 `file_search` @ default LLM | ≤ $0.50                            | ≤ $4.00                                                     | > $6.00     |
| Index throughput (pages/min/worker)   | ≥ 1 500 (text)                      | ≥ 50 (PDF)                                                  | —           |
| Cross-tenant hits                     | **0** (hard)                        | **0** (hard)                                                | —           |
| Prompt-injection blocked              | ≥ 11/12                             | ≥ 11/12                                                     | —           |
| GDPR-delete recall p95                | ≤ 1 000 ms                          | ≤ 1 000 ms                                                  | > 500 ms    |
| Weekly drift on golden nDCG@10        | ≤ 2 pp                              | ≤ 2 pp                                                      | > 1 pp      |

`baseline_v1` = the scorecard captured at Phase-1 ship for `rag_plugin`. It is **not** a
moving target; once captured it is frozen for the life of v1. A new baseline is only
adopted on a deliberate scorecard reset (rare, documented in `CHANGELOG.md`).

### 7.6 Statistical gate

Comparing two configurations (plugin A vs. plugin B, or version N vs. version N+1) uses
a **paired two-sided permutation test, B = 10 000 shuffles** on per-query metric
differences (Smucker / Allan / Carterette, CIKM '07 — the IR-community default).
Minimum query count is **|Q| ≥ 50** per slice (`baseline_v1` on the internal set already
satisfies this). A delta is considered "real" iff p < 0.05 **and** the magnitude
exceeds the threshold in §7.5.

This is the same test that ships in `eval-driven-development` 2026 harnesses
(`promptfoo` / `deepeval` / `braintrust`); we do not reinvent it.

### 7.7 Eval-as-CI wiring

- A new top-level make target `make eval-plugin PLUGIN=<name>` runs the full §7.3 suite on
  a developer box. CI runs the same target nightly per plugin and on every PR that
  touches `internal/mcp/memory/plugins/<plugin>/...`.
- The internal golden sets live in `tests/eval/golden/` (LFS-tracked, PII-scrubbed).
- The harness layer is shared across plugins:
  `internal/mcp/memory/conformance/eval/` exposes `RunRetrievalEval(p Plugin, set string)`,
  `RunRagasEval(p Plugin, set string, judge string)`, `RunRedTeam(p Plugin)`, and
  `RunOpsProbe(p Plugin)`. Plugins are unaware they are being evaluated; the harness uses
  the same `Plugin` interface as production.
- CI artifacts include the scorecard markdown, the raw per-query JSON, and a
  permutation-test result block. PR status check is red if any **hard gate** drops; an
  automated comment highlights any **watch** regression.

### 7.8 Shadow-replay promotion gate (Phase 3)

Before turning `pageindex_plugin` on for a project in production:

1. Dual-write `file_write` to both plugins for that project for 7 days.
2. Replay every `file_search` against both plugins; **only** the active plugin's result
   is served; the other is logged.
3. Score the pair with the same RAGAS and BEAM judges, plus an LLM-judge head-to-head
   "which result better answers the question" (position-swapped, ensemble of two judges
   to mitigate position bias — standard 2026 vendor practice).
4. **Promotion criterion:** `pageindex_plugin` win-rate ≥ 55% with p < 0.05 on the
   period's harvested queries, **and** §7.5 hard gates green for that period's slice.

If win-rate is in `[45%, 55%]`, the project stays on `rag_plugin` (no statistically
significant improvement). Below 45%, file a bug.

### 7.9 Source benchmarks and references

| Reference                                    | Used for                                                              | URL                                                                                                              |
| -------------------------------------------- | --------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------- |
| VectifyAI / PageIndex README                 | Tree-indexing knobs; default LLMs                                     | https://github.com/VectifyAI/PageIndex                                                                            |
| VectifyAI / Mafin2.5-FinanceBench            | FinanceBench-150 harness; published 98.7% baseline                    | https://github.com/VectifyAI/Mafin2.5-FinanceBench                                                                |
| Patronus AI FinanceBench                     | Underlying SEC-filings dataset                                        | arXiv:2311.11944                                                                                                  |
| Wu et al. — LongMemEval                      | Memory benchmark structure (S/M splits, 7 categories)                 | arXiv:2410.10813                                                                                                  |
| Maharana et al. — LoCoMo                     | Conversation-memory benchmark                                         | arXiv:2402.17753                                                                                                  |
| Mem0 memory-benchmarks (BEAM)                | Contradiction / abstention / preference axes                          | https://github.com/mem0ai/memory-benchmarks                                                                        |
| RAGAS v0.4 docs                              | Faithfulness, context_*, answer_correctness formulas                  | https://docs.ragas.io/en/stable                                                                                    |
| Smucker / Allan / Carterette CIKM '07        | Paired permutation test for IR significance                           | https://dl.acm.org/doi/10.1145/1321440.1321528                                                                     |
| OWASP GenAI Data Security 2026 v1.0          | 12-attack prompt-injection catalogue; tenant-isolation expectations   | https://www.lotharschulz.info/wp-content/uploads/OWASP-GenAI-Data-Security-Risks-and-Mitigations-2026-v1.0.pdf    |
| Long-document retrieval survey               | Page/section/multi-hop recall definitions                             | arXiv:2509.07759                                                                                                  |

## 8. Rollout plan

1. **Phase 1 — refactor + harness + baseline (no new plugin behavior).**
   Land in one PR series, all pure Go (per §0):
   - `plugin.Plugin`, `plugin.Manager`, and `rag_plugin` (per §2.1–§2.5).
   - The §4.6 eval harness: Go runner, Go-native RAGAS metrics in
     `conformance/eval/ragas.go`, golden datasets via LFS, `make eval-plugin` and
     `make eval-baseline-rag` targets, the nightly CI workflow. **No external runtime
     anywhere in the harness or its CI runner image** — the eval pipeline is
     `go run ./cmd/eval-plugin` end-to-end, on the same Go toolchain the rest of the
     repo already requires.
   - Run `make eval-baseline-rag` against the Phase-1 tag and **commit the resulting
     `docs/eval/baseline_v1/{rag_plugin_scorecard.md, raw_per_query.jsonl, run_metadata.yml}`
     to the same PR.** This is the frozen reference point §7.5 thresholds depend on.
   - `default_plugin=rag`. Ship and bake for one release.
   - Acceptance: A1, A2, A5, **plus `docs/eval/baseline_v1/rag_plugin_scorecard.md` is
     present in the merged PR with every metric in the §7.4 template filled in**.
2. **Phase 2 — pageindex indexer (off by default).**
   Land `pageindex_plugin` with the Go `Indexer` (§2.6.4) and the Responses-API `LLM`
   (§2.6.5). No new processes, no new container images, no new deploy artifact — the
   plugin links into the existing binary. Default config does not enable it. Run E02/E03
   in staging only. Acceptance: A3, A4, A6, A7, A8, A11, **Q1–Q4, Q9–Q12** (full §7
   scorecard filled in for `pageindex_plugin` against the same golden sets).
3. **Phase 3 — opt-in for selected projects.**
   Enable `pageindex_plugin` for projects shipping long-form documents (`books`,
   `datasheets`, `manuals`). Watch billing and latency metrics. Promotion gate:
   shadow-replay win-rate per §7.8 across two weeks of production traffic.
4. **Phase 4 — remove deprecation shim.**
   In the next minor after Phase 3 stabilizes, delete the `settings.mcp.files.*` shim.
