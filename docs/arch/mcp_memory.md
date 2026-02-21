# MCP-Native Memory Tools Technical Architecture Manual

Last updated: 2026-02-21 (UTC)

## 1. Goal and Decision

### 1.1 Goal

Reduce integration complexity in agent clients by moving memory lifecycle capabilities from client SDK orchestration to MCP server tools, while preserving:

1. Existing tiered memory behavior from `go-utils/agents/memory`
2. Existing tenant isolation and storage guarantees from MCP FileIO
3. Backward compatibility for clients that still use the SDK directly

### 1.2 Decision

Implement **MCP-native memory tools** in `laisky-blog-graphql` and run the memory engine server-side.

Client-side integration changes from:

- client integrates memory engine + storage plugin + lifecycle orchestration

to:

- client calls MCP tools only (`memory_before_turn`, `memory_after_turn`, maintenance/introspection tools)

This is feasible with current codebase primitives.

## 2. Feasibility Analysis (Current State)

### 2.1 MCP server foundations are already complete

Current MCP server already provides:

1. Tool registration and handlers (`internal/mcp/server.go`)
2. Request-scoped auth context injection (`ctxkeys.AuthContext`), built from Authorization header
3. Request-scoped logger injection
4. Unified tool invocation auditing with structured call logs (`recordToolInvocation`)
5. Feature-flag based tool enable/disable (`internal/mcp/settings.go`)
6. `mcp_pipe` composition support with explicit tool switch dispatch

### 2.2 FileIO is production-grade and directly reusable as memory storage substrate

`internal/mcp/files` already provides:

1. Strict tenant isolation by `apikey_hash`
2. Path validation and stable machine-readable error codes
3. Advisory lock based project-level write serialization
4. Deterministic write/delete/rename/list/stat semantics
5. Hybrid search (`file_search`) and indexing outbox worker
6. Call-log redaction for sensitive payloads
7. Rich tests across behavior and e2e-like tool flows

This is sufficient as durable storage for memory artifacts (`events`, `context`, `memory_tiers`, `meta`).

### 2.3 go-utils memory SDK is mature and already aligned

`go-utils/agents/memory` already includes:

1. Full lifecycle APIs: `BeforeTurn`, `AfterTurn`
2. Management APIs: `RunMaintenance`, `ListDirWithAbstract`
3. Canonical session layout and compatibility fallback
4. Tier policies, retention, compaction, summaries
5. Optional LLM heuristic fact extraction/merge
6. Storage abstraction compatible with MCP FileIO semantics

Conclusion: no algorithmic gap. The work is mainly **MCP toolization + server integration + operational hardening**.

## 3. Target Architecture

```mermaid
flowchart TD
		A[Agent Client] -->|memory_before_turn| B[MCP Server Tools]
		A -->|memory_after_turn| B
		A -->|memory_run_maintenance| B
		A -->|memory_list_dir_with_abstract| B

		B --> C[Memory Tool Layer]
		C --> D[Memory Service Adapter]
		D --> E[go-utils memory.StandardEngine]

		E --> F[Storage Adapter]
		F --> G[internal/mcp/files.Service]
		G --> H[(PostgreSQL + FileIO Indexing)]

		E --> I[/memory/{session_id}/... canonical layout]
		B --> J[calllog + redaction + metrics]
```

### 3.1 Design principles

1. **Reuse, do not rewrite**: use `go-utils/agents/memory` as core engine
2. **Tool-first ergonomics**: expose lifecycle as MCP tools
3. **Tenant safety by construction**: all operations require trusted auth context
4. **Compatibility-first**: keep canonical+legacy dual behavior during migration
5. **Operational determinism**: idempotency, retries, observability, bounded payloads

## 4. MCP Tool Contract (v1)

### 4.1 `memory_before_turn`

Purpose: prepare model input with recalled memory + context.

Input:

```json
{
  "project": "tenant-a",
  "session_id": "session-001",
  "user_id": "user-001",
  "turn_id": "turn-123",
  "current_input": ["ResponseItem"],
  "base_instructions": "optional",
  "max_input_tok": 120000
}
```

Output:

```json
{
  "input_items": ["ResponseItem"],
  "recall_fact_ids": ["fact_id"],
  "context_token_count": 1234
}
```

### 4.2 `memory_after_turn`

Purpose: persist turn artifacts, extract/merge facts, update metadata.

Input:

```json
{
  "project": "tenant-a",
  "session_id": "session-001",
  "user_id": "user-001",
  "turn_id": "turn-123",
  "input_items": ["ResponseItem"],
  "output_items": ["ResponseItem"]
}
```

Output:

```json
{
  "ok": true
}
```

### 4.3 `memory_run_maintenance`

Purpose: run compaction/retention/archive/summary refresh.

Input:

```json
{
  "project": "tenant-a",
  "session_id": "session-001"
}
```

Output:

```json
{
  "ok": true
}
```

### 4.4 `memory_list_dir_with_abstract`

Purpose: introspect memory directories and summary docs.

Input:

```json
{
  "project": "tenant-a",
  "session_id": "session-001",
  "path": "",
  "depth": 8,
  "limit": 200
}
```

Output:

```json
{
  "summaries": [
    {
      "path": "/memory/session-001/meta",
      "abstract": "...",
      "updated_at": "2026-02-21T12:00:00Z",
      "has_overview": true
    }
  ]
}
```

### 4.5 `memory_run_turn` (optional convenience tool)

Purpose: one-call utility for less capable clients that want server-orchestrated lifecycle shell.

Behavior:

1. internally runs `memory_before_turn`
2. returns prepared input and a server-issued operation token
3. client later submits output with token to commit (`memory_after_turn` equivalent)

This is optional for v1 and can be deferred if schedule is tight.

## 5. Package and Code Layout

### 5.1 New package

Create `internal/mcp/memory/`:

1. `service.go` (high-level memory service facade)
2. `engine_factory.go` (build `memory.StandardEngine` with config)
3. `storage_adapter.go` (adapt `internal/mcp/files.Service` to `agents/memory/storage.Engine`)
4. `types.go` (request/response DTOs if needed)
5. `settings.go` (load and validate memory settings)
6. `errors.go` (memory-tool typed error mapping)
7. `locks.go` (session-level idempotency/serialization helpers)
8. `service_test.go` and focused unit tests

### 5.2 New MCP tools

Create in `internal/mcp/tools/`:

1. `memory_before_turn.go`
2. `memory_after_turn.go`
3. `memory_run_maintenance.go`
4. `memory_list_dir_with_abstract.go`
5. `memory_tool_helpers.go`

Each tool follows existing pattern: `Definition()` + `Handle()` and returns MCP JSON payload.

### 5.3 Server integration points

Update:

1. `internal/mcp/server.go`: instantiate/register memory tools
2. `internal/mcp/server_tool_handlers.go`: add audited handlers with call logging
3. `internal/mcp/settings.go`: add feature flags
4. `internal/mcp/server.go` `mcp_pipe` switch: include memory tool cases

## 6. Storage Adapter Design

### 6.1 Why adapter instead of MCP loopback calls

Do **not** call MCP tools from memory tools (tool->tool HTTP recursion). Use in-process adapter:

- lower latency
- clearer failure surface
- no nested auth/session complexity
- easier observability

### 6.2 Adapter behavior

Implement `agents/memory/storage.Engine` methods by delegating to `files.Service` with trusted `files.AuthContext`:

1. `Read` -> `files.Service.Read`
2. `Write` -> `files.Service.Write`
3. `Stat` -> `files.Service.Stat`
4. `List` -> `files.Service.List`
5. `Search` -> `files.Service.Search`
6. `Delete` -> `files.Service.Delete`

Constraints:

1. Force `content_encoding=utf-8`
2. Preserve write modes (`APPEND`/`OVERWRITE`/`TRUNCATE`)
3. Keep all timestamps and policy boundaries in UTC

## 7. Concurrency, Idempotency, and Consistency

### 7.1 Problem

`AfterTurn` currently relies on file-based `processed_turn_ids`. Concurrent commits for same session may race.

### 7.2 Required hardening

Add DB-backed idempotency guard for memory commits:

Table proposal:

`mcp_memory_turn_guard(apikey_hash, project, session_id, turn_id, status, updated_at)`

Rules:

1. Unique key `(apikey_hash, project, session_id, turn_id)`
2. Insert with `processing` before engine `AfterTurn`
3. If duplicate with `done`, return success (idempotent)
4. If duplicate with `processing` and fresh timestamp, return `RESOURCE_BUSY` retryable
5. Mark `done` only after successful commit

This preserves exactly-once externally observable semantics under retries.

### 7.3 Session-level serialization

Use advisory lock key `(apikey_hash, project, session_id)` around `AfterTurn` and maintenance operations.

This should be implemented with timeout + retryable error parity with FileIO (`RESOURCE_BUSY`).

## 8. Security and Isolation

1. Use only trusted auth context from request middleware; never parse raw Authorization inside memory tool handlers.
2. All memory operations are tenant-scoped via `apikey_hash + project`.
3. Redact sensitive memory content in call logs similarly to FileIO redaction:
   - redact `current_input`, `input_items`, `output_items`, and any generated memory block text
4. Enforce payload limits:
   - max JSON request bytes per tool call
   - max `current_input`/`output_items` item count
   - max text per content part
5. Keep API keys out of persisted memory files and logs.

## 9. Configuration Model

### 9.1 Configuration philosophy (minimize admin burden)

Memory should be **usable out-of-the-box** with secure and practical defaults.

Admin-facing model:

1. Keep only one required switch: `settings.mcp.tools.memory.enabled`
2. All other options are optional and auto-defaulted
3. Advanced tuning is allowed but not required for normal deployment

### 9.2 Minimal required configuration

For most environments, only this is needed:

```yaml
settings:
  mcp:
    tools:
      memory:
        enabled: true # Enable MCP-native memory tools.
```

### 9.3 Optional configuration keys with default values

The following keys are optional. If omitted, server uses safe defaults.

1. `settings.mcp.memory.recent_context_items` (default: `30`)

- Number of recent context items retained in runtime prompt assembly.
- Larger value improves continuity but increases token cost.

2. `settings.mcp.memory.recall_facts_limit` (default: `20`)

- Max number of memory facts recalled into each turn.
- Prevents prompt bloat while keeping useful long-term memory.

3. `settings.mcp.memory.search_limit` (default: `5`)

- Max number of storage search chunks used for memory recall.
- Keeps latency and prompt size stable.

4. `settings.mcp.memory.compact_threshold` (default: `0.8`)

- Triggers context compaction when estimated tokens exceed this fraction of `max_input_tok`.
- `0.8` is conservative and avoids hard context-window failures.

5. `settings.mcp.memory.l1_retention_days` (default: `1`)

- Retention for short-lived daily facts.
- Auto-expired in UTC.

6. `settings.mcp.memory.l2_retention_days` (default: `7`)

- Retention for medium-lived weekly facts.
- Auto-expired in UTC.

7. `settings.mcp.memory.compaction_min_age_hours` (default: `24`)

- Minimum shard age before raw-event archive compaction.
- Reduces churn and avoids over-frequent archive writes.

8. `settings.mcp.memory.summary_refresh_interval_minutes` (default: `60`)

- Minimum interval for summary refresh operations.
- Balances freshness and write pressure.

9. `settings.mcp.memory.max_processed_turns` (default: `1024`)

- Max in-meta processed turn IDs retained for idempotency history.
- Bounded to avoid metadata growth.

10. `settings.mcp.memory.heuristic.enabled` (default: `false`)

- Enables LLM-assisted fact extraction and merge.
- Default disabled to reduce external dependency, cost, and operational complexity.

11. `settings.mcp.memory.heuristic.model` (default: `openai/gpt-oss-120b`)
12. `settings.mcp.memory.heuristic.base_url` (default: empty)

- Must be an **absolute URL** with scheme and host (for example, `https://api.openai.com` or `https://api.openai.com/v1`).
- Do **not** use domain-only values (for example, `api.openai.com`), because requests require a URL with protocol.
- Path handling:
  - If value already ends with `/v1/responses`, it is used as-is.
  - Otherwise server appends `/v1/responses` automatically.
- Recommended values:
  - Provider root: `https://api.openai.com` (auto-expanded to `/v1/responses`)
  - OpenAI-compatible gateway root: `https://oneapi.example.com` (auto-expanded)
  - Explicit endpoint: `https://oneapi.example.com/v1/responses`
- Avoid query-string or fragment in this field; keep it as clean base endpoint.

13. `settings.mcp.memory.heuristic.timeout_ms` (default: `12000`)
14. `settings.mcp.memory.heuristic.max_output_tokens` (default: `800`)

- Applied only when heuristic is enabled and credentials are configured.

### 9.4 Recommended full example (copy-ready)

```yaml
settings:
  mcp:
    tools:
      memory:
        enabled: true # Single required switch for standard rollout.

    memory:
      # Prompt/context controls
      recent_context_items: 30 # Keep the latest 30 items in active context.
      recall_facts_limit: 20 # Inject up to 20 facts per turn.
      search_limit: 5 # Read up to 5 related chunks from storage search.
      compact_threshold: 0.8 # Compact context at 80% of max_input_tok.

      # Retention controls (UTC)
      l1_retention_days: 1 # Daily facts expire after 1 day.
      l2_retention_days: 7 # Weekly facts expire after 7 days.

      # Maintenance controls
      compaction_min_age_hours: 24 # Archive compaction only for shards older than 24h.
      summary_refresh_interval_minutes: 60 # Refresh .abstract/.overview at most hourly.
      max_processed_turns: 1024 # Bound idempotency history size.

      heuristic:
        enabled: false # Keep off by default; enable only if needed.
        model: openai/gpt-oss-120b
        base_url: '' # Required when heuristic is enabled. Must be absolute URL, e.g. https://api.openai.com or https://oneapi.example.com/v1.
        timeout_ms: 12000
        max_output_tokens: 800
```

### 9.5 Operational recommendations

1. Default profile is recommended for most tenants.
2. Enable heuristic only for tenants that need higher memory extraction quality.
3. Change one parameter at a time and observe `before_turn`/`after_turn` latency and token usage.
4. Keep all retention/expiry logic in UTC to avoid timezone drift.

## 10. Runtime Flow

### 10.1 `memory_before_turn`

1. Validate request DTO
2. Build per-request storage adapter with auth context
3. Build/get memory engine instance with settings
4. Run `engine.BeforeTurn`
5. Return prepared payload

### 10.2 `memory_after_turn`

1. Validate request DTO
2. Acquire idempotency guard + session lock
3. Run `engine.AfterTurn`
4. Mark guard `done`
5. Return success

### 10.3 Maintenance tool

1. Validate request
2. Acquire session lock
3. Run `engine.RunMaintenance`
4. Return summary (`ok=true`, optional counters later)

## 11. Migration Strategy

### Phase 0: Build but disabled

1. Ship code with `settings.mcp.tools.memory.enabled=false`
2. Unit tests + integration tests pass

### Phase 1: Shadow mode

1. Selected clients call memory tools in parallel with existing client-side SDK
2. Compare outputs:
   - recall facts
   - context token count
   - produced memory files

### Phase 2: Controlled cutover

1. Enable memory tools for target tenants
2. Remove client-side direct memory SDK orchestration in those clients
3. Keep fallback path for rollback

### Phase 3: Default-on

1. `memory.enabled=true` by default
2. Client SDK direct mode becomes optional legacy path

## 12. Testing Plan

### 12.1 Unit tests

1. Tool input validation and error mapping
2. Storage adapter method parity tests
3. Idempotency guard behavior under duplicate turn IDs
4. Session lock timeout and retryable errors

### 12.2 Integration tests

1. `before_turn -> after_turn` roundtrip with real DB/FileIO service
2. Retention sweep and compaction behavior
3. Legacy fallback compatibility (`legacy*.json`) during migration
4. Call-log redaction for memory payload fields

### 12.3 Race and reliability

1. concurrent `memory_after_turn` on same `(project, session_id, turn_id)`
2. concurrent `memory_after_turn` on same session different turn ids
3. interrupted maintenance and retry safety

Use:

1. `go test ./internal/mcp/... -count=1`
2. targeted `-race` on memory and tool packages

## 13. Observability and SLOs

### 13.1 Metrics

Add counters/histograms:

1. `mcp_memory_before_turn_total{status}`
2. `mcp_memory_after_turn_total{status}`
3. `mcp_memory_after_turn_duration_ms`
4. `mcp_memory_maintenance_total{status}`
5. `mcp_memory_compaction_total`
6. `mcp_memory_fact_write_total{tier}`
7. `mcp_memory_idempotency_conflicts_total`

### 13.2 Logs

Structured logs with request id, project, session id, turn id, latency, status.
Never log content payloads.

### 13.3 Initial SLO suggestions

1. P95 `memory_before_turn` < 300ms (without remote heuristic LLM)
2. P95 `memory_after_turn` < 500ms
3. Tool success rate >= 99.9% excluding client/network failures

## 14. Open Decisions (Need Product/Platform Confirmation)

1. Whether to include `memory_run_turn` convenience tool in v1 or defer to v1.1
2. Whether heuristic LLM extraction should be enabled by default or opt-in per tenant
3. Whether server should run periodic global maintenance jobs or keep maintenance strictly tool-triggered
4. Retention defaults for production tenants beyond current SDK defaults

If not explicitly decided, recommended defaults are:

1. defer `memory_run_turn`
2. heuristic extraction opt-in
3. maintenance tool-triggered in v1
4. retain current SDK retention defaults

## 15. Implementation Checklist

1. Add `internal/mcp/memory` package and adapter
2. Add memory tool handlers in `internal/mcp/tools`
3. Wire registration + `mcp_pipe` dispatch + feature flags
4. Add idempotency guard migration and lock utility
5. Add call-log redaction support for memory tools
6. Add tests (unit, integration, race-focused)
7. Add operational docs and config examples

This plan delivers MCP-native memory capabilities with minimal client burden while preserving current storage guarantees, compatibility behavior, and production operability.

## 16. Implementation Status (Repository)

Implemented in this repository:

1. Memory service package: `internal/mcp/memory`
2. Memory MCP tools: `internal/mcp/tools/memory_*`
3. MCP server registration and dispatch wiring:

- `internal/mcp/server.go`
- `internal/mcp/server_tool_handlers.go`
- `internal/mcp/settings.go`

4. Startup initialization and settings validation:

- `cmd/api.go`
- `cmd/config_validation.go`

5. Runtime frontend tool availability:

- `internal/web/server.go`
- `web/src/lib/runtime-config.ts`
- `web/src/pages/home.tsx`

6. Redaction coverage for memory payload fields:

- `internal/mcp/memory/logging_redaction.go`
- `internal/mcp/log_redaction.go`

7. Tests:

- `internal/mcp/memory/service_test.go`
- `internal/mcp/log_redaction_test.go`
- `internal/mcp/server_test.go`
