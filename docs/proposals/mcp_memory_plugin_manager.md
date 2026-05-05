---
title: MCP memory plugin manager (rag_plugin + pageindex_plugin)
status: draft
owner: mcp team
created: 2026-05-05
updated: 2026-05-05
revision: r2 — fileio-backed pageindex; gRPC sidecar; system namespace
---

# MCP Memory Plugin Manager — Change Manual

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
reasoning-driven retrieval**:

- Indexing turns each document into a hierarchical JSON tree (Table-of-Contents nodes with
  `node_id`, `title`, `start_index`/`end_index`, LLM summary). Knobs:
  `toc_check_page_num=20`, `max_page_num_each_node=10`, `max_token_num_each_node=20000`.
- Retrieval exposes three primitives — `get_document`, `get_document_structure`,
  `get_page_content(pages="5-7,12")` — to an LLM agent that traverses the tree.
- Persistence is plain JSON in a `workspace/{doc_id}.json` plus a `_meta.json` catalog.
  No vector DB, no embeddings (`grep -r 'embedding\|pgvector\|chroma\|faiss\|pinecone'` in
  the clone returns nothing).
- Dependencies (`requirements.txt`): `litellm==1.83.7`, `pymupdf==1.26.4`, `PyPDF2==3.0.1`,
  `python-dotenv==1.2.2`, `pyyaml==6.0.2`. Default models `gpt-4o-2024-11-20` for indexing,
  `retrieve_model` for queries.

### 1.3 Goal

Refactor the memory backend behind a **plugin contract** so different engines can be plugged
in per-tenant or per-project without changing the public MCP tool schemas. Ship two plugins on
day one:

- **`rag_plugin`** — the existing Postgres + pgvector + BM25 + rerank stack, behavior-preserving.
- **`pageindex_plugin`** — a new vectorless, tree-based engine wrapping PageIndex.

A `PluginManager` selects the active plugin at request scope using settings (default global,
per-project override, per-API-key override).

### 1.4 Non-goals

- No change to the public MCP tool schemas beyond the additive optional `plugin` field
  (§2.4.1).
- No change to `memory_*` session-memory tools (`memory_before_turn`, `memory_after_turn`,
  `memory_run_maintenance`, `memory_list_dir_with_abstract`) — those layer on top of the plugin
  via `FileService` and need no work beyond compile-time fixes.
- No automatic migration of existing tenant data into PageIndex format. New plugin starts empty
  and is opt-in.
- No reimplementation of the PageIndex algorithm in pure Go (see §3.3 — out of scope for v1).
- **Not a vector-DB replacement.** Per the 2026 industry consensus (see §1.5), PageIndex is
  *tier-2* retrieval — invoked after candidate document(s) are chosen. `rag_plugin` remains
  the right answer for at-scale coarse retrieval across an unbounded corpus. The two plugins
  are complementary, selected per-project per the resolution rules in §2.4, not adversaries.

### 1.5 Industry context (2026 sanity check)

Three observations from a 2026 survey of agent-memory and vectorless-RAG systems shape the
design choices below; they are recorded here so future readers can audit the assumptions:

- **Stateless Python sidecar with gRPC server-streaming is the prevailing 2026 pattern**
  (Ray Serve, BentoML, Modal). Cgo / `go-python` is explicitly avoided. Long-running indexing
  benefits from streaming progress events; bare HTTP+SSE is being displaced by gRPC streaming
  in service-mesh stacks. The design in §2.6 reflects this.
- **Memory backends behind a narrow tool surface is the norm** (Mem0: `add/search/update/delete`
  with 19 vector backends + 2 graph backends; Letta, Zep, LangGraph Memory, Anthropic memory
  tool follow the same shape). Per-tenant plugin selection is routed in the host *before*
  tool dispatch — the backend never sees tenant identity except as opaque scope IDs.
- **Reserved prefixes inside a user-visible FS namespace are an acknowledged anti-pattern**
  at scale (AWS S3 multi-tenant guidance; documented prefix/RLS leakage modes). The fix is a
  separate **system namespace** the user API cannot address — enforced in the data path, not
  by convention. §2.6.3 uses this pattern for plugin-internal artifacts (tree JSON, catalog).

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
    SearchModes       []SearchMode  // {"hybrid","semantic","lexical","tree-reasoning"}
    SupportsRandomIO  bool          // false for pageindex (offset/length read into a tree node)
    SupportsRename    bool
    SupportsVersions  bool          // rag_plugin: true (mcp_file_versions); pageindex_plugin: false in v1
    MaxPayloadBytes   int64
    AsyncIndexing     bool          // rag_plugin: true; pageindex_plugin: false (sync, LLM-bound)
    Notes             string
}
```

The MCP server uses this only for soft hints surfaced in tool errors; the tool schemas remain
identical, so callers see uniform behavior.

### 2.3 PluginManager

```go
// internal/mcp/memory/plugin/manager.go (new)
type Manager struct {
    plugins map[string]Plugin    // by Name()
    rules   ResolutionRules      // see §2.4
    fallback Plugin
}

// override is the per-call argument from §2.4.1. Empty or "auto" means "use rules".
func (m *Manager) Resolve(ctx context.Context, auth AuthContext, project, override string) (Plugin, error)
func (m *Manager) StartAll(ctx context.Context) error
func (m *Manager) StopAll(ctx context.Context)  error
func (m *Manager) ForName(name string) (Plugin, error)  // for tests + admin tools
```

### 2.4 Settings & resolution rules

Resolution order (first match wins):

1. **Per-call argument** — optional `plugin` field on every `file_*` tool input (see §2.4.1).
2. Per-API-key override (`settings.mcp.memory.plugin_overrides.api_key_hash[<hash>]`)
3. Per-project override (`settings.mcp.memory.plugin_overrides.project[<project>]`)
4. Default (`settings.mcp.memory.default_plugin`, fallback `"rag"`)

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

- **Omitted or `"auto"`** — fall through to rules 2–4. **Today's default ⇒ `rag_plugin`.** Existing
  callers see no change.
- **Explicit name** (`"rag"` / `"pageindex"`) — bypass all config-level routing for this single
  invocation. Useful for: agents that know which backend a doc lives in; A/B comparison of the
  two engines on the same query; force-routing a `file_write` of a long PDF into PageIndex even
  if the project default is RAG.
- **Unknown name** — return `INVALID_ARGUMENT` with `available_plugins=[...]` in
  `structuredContent` so the caller can self-correct.
- **Cross-plugin reads** — if a path was written via plugin A but the call specifies plugin B
  and the path does not exist there, the response is `NOT_FOUND` with a hint
  `("path exists under plugin=A; pass plugin=A to read it")`. We do not silently fall back
  across plugins (that would defeat tenant isolation between engines and surprise the caller).
- **Mutations are sticky** — a `file_write` under plugin X stores the path under X *only*. There
  is no implicit migration. A subsequent `file_read` without an explicit `plugin` will resolve
  per the rules; if that resolves to a different plugin, the read returns `NOT_FOUND` with the
  hint above.

The field is forwarded by the tool adapter into `Manager.Resolve(ctx, auth, project, override)`
and recorded on the request log entry so per-plugin call volumes are observable.

New settings keys:

```yaml
settings:
  mcp:
    memory:
      default_plugin: "rag"            # "rag" | "pageindex"
      plugin_overrides:
        project:
          books:    "pageindex"
          datasheet:"pageindex"
        api_key_hash: {}
      plugins:
        rag: {}                         # current settings.mcp.files.* re-rooted under here
        pageindex:
          backend: "grpc_sidecar"       # "grpc_sidecar" (prod) | "subprocess" (dev only)
          grpc_sidecar:
            target:           "unix:///run/laisky/pageindex.sock"
            tls:              false
            timeout_index:    "5m"
            timeout_query:    "60s"
            retry:
              max_attempts:    3
              initial_backoff: "250ms"
          subprocess:
            python:        "/opt/laisky/pageindex/.venv/bin/python"
            entrypoint:    "pageindex_sidecar.server"   # serves on a private UDS
            timeout_index: "5m"
            timeout_query: "60s"
          # LLM is provider-agnostic via litellm-style URLs; no provider hard-coded in code.
          llm:
            indexing_model:  "openai/gpt-4o-2024-11-20"
            retrieve_model:  "openai/gpt-4o-2024-11-20"
            api_key_env:     "OPENAI_API_KEY"
            base_url:        ""        # empty = provider default
          tree_query:
            max_steps:        8
            max_tokens:       20000
            candidate_docs:   5
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

Located at `internal/mcp/memory/plugins/pageindex`. PageIndex is Python-only; we do not
reimplement its LLM-driven indexing in Go (cost/quality risk too high for v1).

Two design choices distinguish this from a naïve port:

- **All persistence rides on the existing fileio.** Raw bytes (the user's PDF/MD) are stored
  via the *same* `*files.Service` that backs `rag_plugin`'s user-facing layer, so
  `file_read`/`file_stat`/`file_list` against a `pageindex_plugin`-backed project behave
  identically to today for those verbs. PageIndex's own metadata (per-doc tree JSON, the
  `_meta.json` catalog, the path↔doc_id index) lives in a **separate system namespace** the
  user API cannot address (see §2.6.3). Net effect vs. r1 of this proposal: no
  `workspace_root` directory, no new `mcp_pageindex_docs` table, no new on-disk layout.
- **The Python sidecar is stateless and speaks gRPC.** The sidecar never touches disk: the
  Go plugin pulls raw bytes from fileio, streams them to the sidecar over gRPC, receives the
  indexed JSON tree back, then persists it via fileio in the system namespace. This matches
  the 2026 reference pattern (Ray Serve / BentoML / Modal: stateless inference + external
  state) and avoids cgo entirely.

The single backend is `grpc_sidecar`. A `subprocess` fallback (one-shot
`python -m pageindex.cli` per call, JSON over stdin/stdout, no streaming) is retained
purely for **dev/test ergonomics** when the sidecar isn't running; it is not a production
path.

#### 2.6.1 Mapping `FileService` → PageIndex (fileio-backed)

Notation: `userFS` is the user-facing fileio (the same instance `rag_plugin` wraps);
`sysFS` is `userFS` accessed through the system-namespace handle (§2.6.3).

| Tool          | Behavior under `pageindex_plugin`                                                                                                                                                                                                                |
| ------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `file_write`  | 1) `userFS.Write(project, path, content, mode)` — exactly today's path. 2) If `path` ends with `.pdf`/`.md`: stream content to sidecar via `IndexDocument` gRPC; on final tree, `sysFS.Write("pageindex/<doc_id>.json", treeJSON, TRUNCATE)` and update `sysFS:"pageindex/_meta.json"` plus the path↔doc_id mapping in `sysFS:"pageindex/index.json"`. Other extensions (`.txt`, `.json`): step 1 only. |
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
   a. `sysFS.Read("pageindex/<doc_id>.json")` — the cached tree JSON. No sidecar call needed.
   b. Ask the LLM (`retrieve_model`) "given this tree and this query, list up to N page ranges
      most likely to contain the answer; output JSON `[{pages,reason}]`."
   c. For each range, call the sidecar's `GetPageContent(doc_id, pages)` — content is in the
      cached tree JSON, so this is in-process JSON traversal, not a network round-trip in v1.
3. Convert each returned `{page, content}` into a `ChunkEntry` with synthesized byte offsets
   (page-level offsets resolved via the cached `pages` array; markdown uses `line_num`).
   `Score` is a normalized rank from the LLM (fallback: position-based decay).
4. Merge and sort across docs; truncate to `limit`.

This loop is fully encapsulated inside the plugin — the MCP tool schema is unchanged. Cost
and latency are bounded by the budgets above; budgets are exposed as settings and emitted as
billing metadata (`mcp.memory.pageindex.tokens_in`, `tokens_out`, `llm_calls`).

The LLM client used in step 2.b is **provider-agnostic** behind a small interface
(`retrieve.LLM`) so a project can pin OpenAI / Anthropic / a local model without changing
the sidecar contract. Defaults sit in settings (§2.4); no provider is hard-coded in code.

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

#### 2.6.4 Sidecar contract & packaging

The sidecar is a **stateless gRPC service** (Python, FastAPI dropped). All disk I/O lives on
the Go side; the sidecar receives bytes and returns parsed structures.

```protobuf
// third_party/pageindex_sidecar/proto/pageindex.proto
syntax = "proto3";
package pageindex.v1;

service PageIndex {
  // Streaming: client streams Chunk frames of file bytes; server streams progress
  // events (tree-extraction phases, token spend, partial sub-trees) and finally
  // a single FinalTree.
  rpc IndexDocument(stream IndexRequest) returns (stream IndexEvent);

  // Pure functions over an already-indexed tree (the Go plugin holds the JSON; the
  // sidecar provides the canonical traversal/serialization to keep parity with
  // upstream PageIndex semantics).
  rpc GetDocumentStructure(GetStructureRequest) returns (StructureResponse);
  rpc GetPageContent      (GetPageRequest)      returns (PageContentResponse);

  rpc Healthz(google.protobuf.Empty) returns (HealthResponse);
}

message IndexRequest {
  oneof body {
    IndexHeader header = 1;   // sent first: doc_type, doc_name, model, knobs
    Chunk       chunk  = 2;   // ≤ 1 MiB per frame
  }
}
message IndexEvent {
  oneof event {
    Progress  progress  = 1;  // {phase, percent, tokens_in, tokens_out}
    Tree      partial   = 2;  // optional incremental tree
    FinalTree final     = 3;  // exactly once at the end
    Error     error     = 4;
  }
}
```

Operational profile:

- Listens on `unix:/run/laisky/pageindex.sock` by default (lower latency than TCP for the
  sidecar-on-same-host case), TCP `127.0.0.1:18901` available for separate-host deploys.
- Health: `Healthz` returns `OK` once the LLM client is initialized; readiness probe wraps it.
- Packaged in `third_party/pageindex_sidecar/` with `server.py`, `requirements.txt`
  (pinned PageIndex commit + `grpcio`, `grpcio-tools`, `litellm`), and a **Debian**-based
  `Dockerfile` (per memory: never Alpine).
- Go plugin uses `grpc-go` with retry policy `{maxAttempts: 3, initialBackoff: 250ms}` for
  `UNAVAILABLE`. Streamed `IndexEvent.progress` is forwarded to a structured logger and to
  per-call billing metadata; clients still see the result as a single tool response (the
  plugin awaits `FinalTree` before returning).
- For dev: a `subprocess` mode launches the sidecar inline as a Python child process listening
  on a private UDS, so contributors don't need a separate Compose file. This is the only
  retained vestige of the old "subprocess vs. http" choice.

### 2.7 Wiring changes

- [cmd/api.go:408-447](../../cmd/api.go#L408-L447): replace direct `files.NewService`
  injection with `plugin.Manager.MustBuild(cfg)`. The manager constructs each enabled plugin
  (today: rag always on; pageindex on if config block present) and starts them.
  - The single `*files.Service` instance is built once and **shared** by both plugins:
    `rag_plugin` wraps it directly; `pageindex_plugin` receives it as `userFS` and a
    `SystemFS` handle scoped to `system_owner="pageindex"`.
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
    userFS *files.Service
    sysFS  files.SystemFS   // bound to "pageindex"
    grpc   PageIndexClient  // gRPC sidecar client
    cfg    Settings
}
```

Recursion is **structurally impossible**: the plugin doesn't know about the `Manager`, and
the `SystemFS` it holds has its own SQL scoping that cannot land back in the user namespace.

## 3. Risks & open questions

### 3.1 Cost

PageIndex query is LLM-bound. A typical search over a 200-page doc is 1× structure call
(small, cached) + 1 LLM "pick ranges" + 1–3 `get_page_content` reads + answer composition
upstream. We **must** cap with `tree_query.max_steps` and emit per-call token billing so
operators can see the bill before it surprises them.

### 3.2 PageIndex API stability

Pinning to PageIndex commit X (recorded in `third_party/pageindex_sidecar/requirements.txt`).
The library exposes a tiny surface (`index`, `get_document`, `get_document_structure`,
`get_page_content`); upstream churn risk is low.

### 3.3 Pure-Go future

A pure-Go reimplementation is attractive (fewer moving parts, no Python sidecar) but the
indexing path is genuinely LLM-driven. Defer to v2 once the sidecar's metric volume lets us
size the LLM contract.

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

### 3.6 LLM provider lock-in

PageIndex upstream defaults to OpenAI prompts and chat-template assumptions; some prompts
contain GPT-isms that other models render less reliably. Mitigations:

- Sidecar uses litellm for transport (already a PageIndex dependency), so OpenAI / Anthropic /
  local OSS endpoints all work.
- The `tree_query` prompt that drives candidate-range selection lives **on the Go side** in
  the plugin, not in the sidecar — so we own the prompt that costs us tokens. The sidecar's
  prompts (tree extraction) remain whatever PageIndex ships, pinned to a commit.

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
| `internal/mcp/memory/plugins/pageindex/plugin.go`                 | Plugin entrypoint; holds `userFS`, `SystemFS`, gRPC client                           |
| `internal/mcp/memory/plugins/pageindex/grpc_backend.go`           | gRPC client wrapping the sidecar (default backend)                                   |
| `internal/mcp/memory/plugins/pageindex/subprocess_backend.go`     | Dev-mode: spawn local sidecar over UDS; same gRPC stub as production                 |
| `internal/mcp/memory/plugins/pageindex/sysstore.go`               | `SystemFS`-based reads/writes for `pageindex/{<doc_id>.json, _meta.json, index.json}`|
| `internal/mcp/memory/plugins/pageindex/search_loop.go`            | Tree-reasoning search (§2.6.2) + provider-agnostic `retrieve.LLM`                    |
| `internal/mcp/memory/plugins/pageindex/{...}_test.go`             | Unit + golden tests (canned tree, byte-offset synthesis, budget exhaustion)          |
| `internal/mcp/memory/conformance/suite.go`                        | Shared "every plugin must pass" test suite                                           |
| `internal/mcp/files/system_fs.go`                                 | `SystemFS` interface + `*Service.SystemNamespace(owner)` constructor (§2.8)          |
| `internal/mcp/files/system_fs_test.go`                            | Isolation tests: SystemFS cannot read/write user namespace; user FS cannot see system rows |
| `third_party/pageindex_sidecar/proto/pageindex.proto`             | gRPC contract (§2.6.4)                                                               |
| `third_party/pageindex_sidecar/server.py`                         | Stateless gRPC server wrapping `PageIndexClient`                                     |
| `third_party/pageindex_sidecar/requirements.txt`                  | Pinned deps (PageIndex commit, `grpcio`, `grpcio-tools`, `litellm`)                  |
| `third_party/pageindex_sidecar/Dockerfile`                        | Debian-based image                                                                   |
| `third_party/pageindex_sidecar/README.md`                         | Operator docs (Compose, UDS path, healthz)                                           |
| `internal/mcp/memory/plugins/pageindex/proto/pageindex_grpc.pb.go`| Generated stubs (committed; regen via `make proto`)                                  |
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
| `Makefile` (or equivalent)                                        | New `proto` target running `protoc` against `third_party/pageindex_sidecar/proto/`                                                                                                                                                                                                                                              |
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
- Old r1 keys (`workspace_root`, `http_sidecar.*`) are not in any released config; if they
  appear in a config file we error at startup pointing to §2.4 in this proposal.

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
| R08 | A writes P; concurrently the engine's index worker / sidecar restarts                                 | After restart, a read of P returns either the bytes or `UNAVAILABLE` (transient); never empty success. Within the freshness window, a search re-derives P at most once (no double-billing).                                                                                                                                  |
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
| E02 | Operator runs with `default_plugin=pageindex` and the sidecar up, writes a 100-page PDF, then queries it                 | `file_search` returns at least one chunk whose pages overlap the answer; total wall-clock for write→search ≤ §7.5 thresholds                                                                      |
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
| P01 | `grpc_backend` happy path: in-process `bufconn` gRPC server returns a canned `IndexEvent.FinalTree`. Asserts streamed `Chunk` frames match input bytes byte-for-byte.               |
| P02 | `subprocess_backend` happy path: fake Python script speaks the same gRPC contract over a UDS — captures the inbound stream, asserts shape.                                          |
| P03 | `Search` loop given canned tree + canned `GetPageContent`: asserts merge order, byte-offset synthesis, `limit` truncation.                                                          |
| P04 | `Search` loop budget exhaustion (`max_steps=2`) → returns partial result with `truncated=true`, no error. (User-observable side covered by C-row + Q7.)                              |
| P05 | `Search` loop with malformed LLM JSON → degrades to "fetch first node of each candidate" fallback. (User-observable side covered by C-row.)                                          |
| P06 | `Write(.pdf, OVERWRITE@offset=10)` → `INVALID_ARGUMENT`. (User-observable side covered by C09b.)                                                                                     |
| P07 | `Write(.pdf, TRUNCATE)` then `Read(.pdf)` returns identical bytes via `userFS`.                                                                                                      |
| P08 | `Delete` removes user row from `userFS` and the `pageindex/<doc_id>.json` row from `SystemFS`; index mapping updated atomically.                                                     |
| P09 | `Rename` updates `userFS` row + index mapping; tree JSON content untouched.                                                                                                          |
| P10 | Index-mapping mutation is serialized: 10 concurrent writes to distinct user paths in the same project all succeed; final mapping has 10 entries.                                    |
| P11 | Sidecar unreachable → `UNAVAILABLE` from gRPC, retried per policy, then surfaces as a tool-level `unavailable` error. (User-observable side covered by C-rows.)                      |
| P12 | LLM-provider switch: setting `llm.indexing_model="anthropic/claude-..."` routes through litellm; no OpenAI client is instantiated.                                                   |
| P13 | `WriteOpts.SkipRAGIndex=true` means the index worker observes no new job for the row.                                                                                                |
| P14 | Streaming progress: index events are forwarded to a captured logger and to billing metadata; the final tool response still arrives as one unit.                                      |

#### 5.5.3 Manager / settings regression

| ID  | Internal test (informational)                                                                                                                                  |
| --- | -------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| M01 | API-key override beats project override beats default.                                                                                                          |
| M02 | Unknown plugin name in config returns startup error.                                                                                                            |
| M03 | `StartAll` failure on one plugin aborts startup with a wrapped error naming the plugin.                                                                         |
| M04 | Settings shim: legacy `settings.mcp.files.*` is read into `settings.mcp.memory.plugins.rag.*`.                                                                  |
| M05 | Settings shim emits exactly one WARN per process (not per request).                                                                                             |
| M06 | `Capabilities.SupportsVersions=false` makes the versions-aware tool path fall back gracefully.                                                                  |
| M07 | Per-call `plugin` arg beats API-key override beats project override beats default.                                                                              |
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
   When the conformance suite runs against a plugin (real DB; faked sidecar in CI for
   `pageindex`), every C-row and every R-row produces exactly one of the documented
   user-observable outcomes.

A5. **Legacy config remains valid.**
   When an operator boots the server with a config that sets only the legacy
   `settings.mcp.files.*` keys, the server boots, prints exactly one deprecation warning
   on startup, and the user-visible behavior of all tool calls is identical to a config
   that sets the new keys equivalently.

A6. **A new operator can run `pageindex` end-to-end from the manual.**
   When an operator who has never seen the project follows the steps in
   `third_party/pageindex_sidecar/README.md` on a 2-vCPU box, the readiness probe is
   green within 30 s and the §5.3 E02 scenario succeeds on the first try.

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
   config (or leaves the sidecar offline), every tool call's observable outcome matches
   the pre-refactor system on the same input. No migration leaves behind a user-visible
   change.


A11. **Choice of LLM provider is invisible to the agent.**
   When an operator configures the indexing/retrieval LLM to a non-OpenAI provider
   (e.g., `llm.indexing_model="anthropic/claude-..."`), `pageindex_plugin` indexing and
   search succeed and produce results of comparable quality (within the §7.5 watch
   thresholds). The agent's request/response shape never depends on the provider
   choice.

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

#### 7.3.2 End-to-end generation quality (RAGAS v0.4)

- **Harness:** `ragas==0.4.x`, judge `gpt-4o-mini`, embedding `text-embedding-3-small`
  (matching upstream defaults so our numbers are comparable).
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

1. **Phase 1 — refactor under feature flag (no new behavior).**
   Land `plugin.Plugin` + `plugin.Manager` + `rag_plugin` only. `default_plugin=rag`. Ship,
   bake for one release. Acceptance: A1, A2, A5, **plus the §7 baseline scorecard for
   `rag_plugin` is captured and published as the v1 reference point**.
2. **Phase 2 — pageindex sidecar (off by default).**
   Land `pageindex_plugin` + sidecar. Default config does not enable it. Run E02/E03 in
   staging only. Acceptance: A3, A4, A6, A7, A8, **Q1–Q4, Q9–Q12** (full §7 scorecard
   filled in for `pageindex_plugin` against the same golden sets).
3. **Phase 3 — opt-in for selected projects.**
   Enable `pageindex_plugin` for projects shipping long-form documents (`books`,
   `datasheets`, `manuals`). Watch billing and latency metrics. Promotion gate:
   shadow-replay win-rate per §7.8 across two weeks of production traffic.
4. **Phase 4 — remove deprecation shim.**
   In the next minor after Phase 3 stabilizes, delete the `settings.mcp.files.*` shim.
