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
| Rendered fetch | `web_fetch`, $0.0001 | `WebFetch`, same price, `output_markdown` selectable | GraphQL | M / Q |
| Context extraction | `extract_key_info`, $0.002 | `ExtractKeyInfo`, same price | GraphQL | M / Q |
| Semantic/lexical tool discovery | `find_tool`, $0.002 for every mode in current handler | GraphQL has its own schema introspection | Inspector/MCP | M |
| Pipeline orchestration | `mcp_pipe`, zero; child tools bill separately | No field | Inspector/MCP | M on parent and each child |
| File metadata | `file_stat`, zero | `FileStat`, zero | MCP | M / Q |
| File content read | `file_read`, zero | `FileRead`, zero | MCP | M / Q |
| File content write | `file_write`, zero | `FileWrite`, zero | MCP form; HTTP editor | M / Q / H |
| File deletion | `file_delete`, zero | `FileDelete`, zero | MCP | M / Q |
| File rename | `file_rename`, zero | `FileRename`, zero | MCP | M / Q |
| File listing | `file_list`, zero | `FileList`, zero | MCP | M / Q |
| File search | `file_search`, zero | `FileSearch`, zero | MCP | M / Q |
| History metadata | `file_list_versions`, zero | `FileListVersions`, zero | HTTP history dialog | M / Q / H |
| History content | `file_read_version`, zero | `FileReadVersion`, zero | HTTP history dialog | M / Q / H |
| History restore | `file_restore_version`, zero | `FileRestoreVersion`, zero | HTTP history dialog | M / Q / H |
| Memory recall | `memory_before_turn`, zero | `MemoryBeforeTurn`, zero | MCP | M / Q |
| Memory persistence | `memory_after_turn`, zero | `MemoryAfterTurn`, zero | MCP | M / Q |
| Memory maintenance | `memory_run_maintenance`, zero | `MemoryRunMaintenance`, zero | MCP | M / Q |
| Memory directory summary | `memory_list_dir_with_abstract`, zero | `MemoryListDirWithAbstract`, zero | MCP | M / Q |
| Ask human | `ask_user`, zero | No field | HTTP human-response UI | M / H; different roles |
| Consume queued directive | `get_user_request`, zero | No field | HTTP queue-management UI | M / H; producer and consumer differ |

GraphQL FileIO and memory reach the same services through the same
client-precondition gate (`internal/mcp/tools.ConditionalFileService`) and the
same project-selected plugin routing as MCP, so a mutation without
`expected_version` or `create_only` is refused on every interface. The GraphQL
fields stay in the schema regardless of `settings.mcp.tools.*`; each one reports
`SEARCH_BACKEND_ERROR` when its own backend is absent. Byte counts, offsets and
history identifiers use the `BigInt` scalar or decimal strings, because
GraphQL's `Int` is 32-bit.

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

### Billing-outcome contract

`oneapi.CheckUserExternalBilling` now returns a classified `*oneapi.BillingError`
instead of an opaque wrapped error, so every caller can tell the three cases
apart. `oneapi.ClassifyBillingOutcome` reads the classification through any
wrapping, and an unrecognized error resolves to `unknown` rather than `denied`,
because an unrecognized failure is not evidence that nothing was charged.

| Outcome | Remote condition | `Charged()` | Recorded `Cost` | Safe to retry |
| --- | --- | --- | --- | --- |
| `accepted` | HTTP 200 | yes | configured price | no; already applied |
| `denied` | HTTP 4xx — invalid key, exhausted balance, rate limited | no | 0 | yes, by a later explicit action |
| `unknown` | timeout, transport failure, HTTP 5xx | yes | configured price, flagged indeterminate | **no**; may already be applied |
| `not_attempted` | request never left this process | no | 0 | yes |

Both interfaces record the outcome in the existing `parameters` JSONB column
under the reserved `_billing` key (`calllog.BillingMetadataKey`), so no schema
migration is required. The recorded `Cost` follows the **billing** outcome, not
the tool result:

- An accepted consume followed by a provider failure stays charged. Previously
  MCP recorded zero here, which hid a real charge and made the local log
  unusable for reconciliation.
- A denied consume records zero on both interfaces.
- An unresolved consume records the price **and** `indeterminate: true`. Such a
  row is not a receipt; it requires manual reconciliation against the billing
  service. It is never presented as free and is never replayed automatically.
- A request that failed before billing records `not_attempted` with the
  configured price for reference and a zero cost.

GraphQL now also writes an audit row when billing denies or cannot be resolved.
Previously it returned before its local record, so a denial left no trace at all
while MCP recorded one.

Configured per-call prices come from `oneapi.SharedToolPrices()` through runtime
`pricing` metadata. The homepage validates exact decimal strings; missing data is
shown as unknown, never as free.

### Still outstanding

- Dedicated HTTP management calls do not use the MCP wrapper and are audited by
  their own handlers.
- Reconciling an `unknown` row against the billing service is an operational
  procedure, not an automated one. Nothing in this repository issues a refund or
  re-posts a consume.
- Per-user authorization and live backend health remain outside the runtime
  `interfaces` catalog, which describes configured adapters only.

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
