# File history through MCP

These adapters expose the same retained, tenant/project/path/owner-scoped history
as the HTTP editor. MCP and HTTP are peers over shared storage and project-selected
writers. Turning MCP FileIO off does not turn the HTTP history API off.

## Operations

| Tool | Required input | Other input | Result |
| --- | --- | --- | --- |
| `file_list_versions` | `project`, `path` | `limit` (1–200, default 50), `before_id` | `versions` metadata, `has_more`, optional `next_cursor` |
| `file_read_version` | `project`, `path`, `history_id` | None | `history_id`, `content`, `content_encoding`, `size`, UTC `created_at` |
| `file_restore_version` | `project`, `path`, `history_id`, exactly one mutation condition | `expected_version` or `create_only=true`; optional `plugin` | `bytes_written`, new live `version` |

IDs are canonical positive decimal **strings**, bounded by the storage BIGINT
range. Never parse them into a JavaScript number, pass an exponent/leading zero,
or confuse them with an `incarnation:revision` file token.

List pages are ordered by descending immutable row ID. Pass `next_cursor` from
one page as the next `before_id`. Newer insertions do not shift older pages.
Retention can still remove entries between requests; paging is not a pinned
history snapshot or a retention lease. Listing returns metadata only and reads
at most `limit + 1` rows.

## Restore protocol

Read and review the current live file, obtaining its content and version from
one snapshot. Select a retained history ID, preview the immutable bytes, and
restore against the original live version:

```json
{
  "name": "file_restore_version",
  "arguments": {
    "project": "notes",
    "path": "/state.json",
    "history_id": "9007199254740993",
    "expected_version": "76c3e40a3c4b4f5d9c2617d1b8094593:42"
  }
}
```

Use actual IDs/tokens returned by your server. The example values are synthetic.
For a deleted file with retained history, explicitly choose `create_only=true`
instead of `expected_version`. This cannot replace an existing file. Retrying a
successful create-only restore conflicts rather than restoring again.

The adapter does not fetch a newer token before writing. It reuses the existing
condition-aware project/plugin resolver and calls the selected writer with
TRUNCATE. The writer validates the original condition atomically with content,
revision, historical preimage and outbox updates. A failed precondition has no
mutation side effects. Historical bytes are read outside a long-running plugin
transaction; the final write, not that read, enforces the live version.

A PageIndex processing error can occur after the file content commit; the existing
plugin contract does not promise rollback of every post-write error. This change
does not add operation-ID deduplication or automatic retries. On an ambiguous
error, inspect the live file before another explicit edit.

Historical invalid UTF-8 is returned as base64 with `content_encoding="base64"`.
Do not interpret it as literal UTF-8 content. The text-only restore rejects
invalid bytes rather than replacing them with U+FFFD. Ordinary UTF-8 restore
preserves original bytes and receives a new live revision/incarnation as needed.

## Registration and testing

Application startup injects the shared history reader through
`mcp.WithFileHistoryReader`. A backend-only `NewServer` caller can omit it;
missing history dependencies never advertise nonfunctional history tools.
When injected, the three adapters use the ordinary tool registry, discovery and
zero-price audit wrapper. Their content is included in FileIO log redaction.

`concurrency_history_tools_test.go` supplies RAG/PageIndex, independent database
pools, stale restore/no-side-effects, exact IDs, insertion between pages,
delete/recreate, malformed IDs and old binary history cases. The HTTP registrar
and live MCP discovery have separate tests. These Go tests must be executed with
the repository's Go toolchain and PostgreSQL fixture before production acceptance.
