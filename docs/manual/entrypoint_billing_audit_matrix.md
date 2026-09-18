# Shared-tool interface and billing/audit inventory

Scope: shared tools in PR #49, not the unrelated blog, SSO, Telegram or Arweave
administration API. MCP and GraphQL are peer adapters. `settings.mcp.tools.*`
controls MCP registration only. Browser pages use either GraphQL, MCP or a
separate HTTP API; the browser itself must not add a second charge.

The runtime `interfaces` object is assembled from actually registered MCP tool
names and independently configured backend services. It describes adapter
configuration, **not** per-user authorization, provider health or a successful
operation. Authenticated `tools/list` is the relevant per-client catalog. Public
server-card examples are not a live capability guarantee.

## Operation matrix

`M` = existing `executeToolHandler` local call-log wrapper and centralized
billing/fallback audit. `Q` = GraphQL resolver's existing local record after a
provider invocation. `H` = dedicated HTTP handler; not an MCP invocation.
Prices below are the code defaults in `library/billing/oneapi/oneapi.go`, not a
promise about future deployment settings. Zero means no positive wrapper price;
backend indexing/model resources can still have operational cost.

| Operation | MCP / wrapper price | GraphQL | Browser transport | Audit path |
| --- | --- | --- | --- | --- |
| Web search | `web_search`, $0.005 | `WebSearch`, same price | GraphQL | M / Q |
| Rendered fetch | `web_fetch`, $0.0001 | `WebFetch`, same price, Markdown only | GraphQL | M / Q |
| Context extraction | `extract_key_info`, $0.002 | `ExtractKeyInfo`, same price | GraphQL | M / Q |
| Semantic/lexical tool discovery | `find_tool`, $0.002 for every mode in current handler | GraphQL has its own schema introspection | Inspector/MCP | M |
| Pipeline orchestration | `mcp_pipe`, zero; child tools bill separately | No field | Inspector/MCP | M on parent and each child |
| File metadata | `file_stat`, zero | Not implemented | MCP | M |
| File content read | `file_read`, zero | Not implemented | MCP | M |
| File content write | `file_write`, zero | Not implemented | MCP form; HTTP editor | M / H |
| File deletion | `file_delete`, zero | Not implemented | MCP | M |
| File rename | `file_rename`, zero | Not implemented | MCP | M |
| File listing | `file_list`, zero | Not implemented | MCP | M |
| File search | `file_search`, zero | Not implemented | MCP | M |
| History metadata | `file_list_versions`, zero | Not implemented | HTTP history dialog | M / H |
| History content | `file_read_version`, zero | Not implemented | HTTP history dialog | M / H |
| History restore | `file_restore_version`, zero | Not implemented | HTTP history dialog | M / H |
| Memory recall | `memory_before_turn`, zero | Not implemented | MCP | M |
| Memory persistence | `memory_after_turn`, zero | Not implemented | MCP | M |
| Memory maintenance | `memory_run_maintenance`, zero | Not implemented | MCP | M |
| Memory directory summary | `memory_list_dir_with_abstract`, zero | Not implemented | MCP | M |
| Ask human | `ask_user`, zero | No field | HTTP human-response UI | M / H; different roles |
| Consume queued directive | `get_user_request`, zero | No field | HTTP queue-management UI | M / H; producer and consumer differ |

Dedicated FileIO routes are `GET /tools/file_io/api/versions`,
`GET /tools/file_io/api/versions/{id}/content`, `PUT /tools/file_io/api/file`, and
`POST /tools/file_io/api/versions/{id}/restore`, plus the configured API prefix.
They share tenant-scoped storage and condition-aware selected plugin writers,
not MCP registration. The application owns the human-request hold manager even
when no MCP server is built.

## Billing semantics and remaining reconciliation

The named billing “checker” posts `phase=single` and `add_used_quota` to
`/api/token/consume`; it is not just a local preflight or balance query. Search,
fetch and extraction call it before the provider. No new billing calls or
refund behavior are added by this continuation.

Important outstanding differences remain and prevent claiming full G06 acceptance:

- MCP currently records zero local `Cost` on a tool/provider failure, even after
  a successful positive consume; GraphQL records its price after an attempted
  provider call regardless of provider success. Local logs alone are therefore
  not a reliable receipt or refund ledger. Fixing this requires an explicit
  accepted/denied/unknown billing-outcome contract and tests, not simply changing
  the displayed cost to match the other transport.
- MCP can emit a zero-cost centralized audit for invocations that did not reach
  billing. GraphQL early validation/auth/billing failures return before its local
  provider record. Dedicated HTTP management calls do not use the MCP wrapper.
- A billing timeout can have an unknown remote outcome. Do not automatically
  replay it, assert “uncharged,” or infer a refund from a failed provider result.
- Configured per-call prices now come from `oneapi.SharedToolPrices()` through
  runtime `pricing` metadata. The homepage validates exact decimal strings for
  search, fetch and extraction; missing data is not presented as free. This fixes
  the duplicate UI tariff, not billing receipts or unknown consume outcomes.
  Targeted behavior evidence and outstanding full-application acceptance are in
  [the follow-up ledger](pr49_followup_20260918.md).

## Privacy follow-up implemented here

The previous origin-only `URLForLog` helper was not sufficient: the MCP raw-body
and persisted-argument paths did not always invoke it. This continuation applies
recursive redaction to real `tools/call`, legacy `call_tool`, and nested pipeline
arguments. It masks URL path/query/credentials, file content, memory input/output
and extraction materials in logging copies. Malformed/truncated JSON is redacted
rather than returned verbatim. Persisted MCP parameters pass through the same
policy; actual operation arguments are never mutated.

Behavior tests fail on the original redactor and pass on the replacement.
Execution is an isolated standard-library dependency closure, not a logger or
PostgreSQL integration run. Free-form upstream error messages, all other log
sinks, billing receipts and worker-side logs still require their own review.
