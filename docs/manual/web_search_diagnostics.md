# Web search failure diagnostics

## Incident evidence

Task20261013-02 investigated `graphql.laisky-graphql.mcp.web_search`, `web_search failed`, at `2026-10-13T04:21:50.160442029Z`, whose alert fields were `{}`.

The running production image index is `sha256:6bdd547f6c727a702475dc96ba980bb7ada62ee3df31e64a41454ff617a10762`. Its linux/amd64 manifest config matches the running container image `sha256:4bc81ba89f52175e0b48eed55aa3c055df212cbddc29253887088fb2f0663054`. Public BuildKit provenance and [the workflow run](https://github.com/Laisky/laisky-blog-graphql/actions/runs/38112178357) identify source `ff2a61a5543f579ce738e58f2bedf1b201ac7414`. Provenance was read and matched; signature authenticity was not independently verified. Binary metadata identifies go-utils/v6 `v6.3.2-0.20261008165128-506ec9758d5f`.

At that exact source, the search error callsite supplies `zap.Error(err)` and `query_len`. The installed `WebFetchAlertHook` forwards nil fields for all callsites except the approved fetch event. Therefore this search alert's empty payload is explained by downstream filtering, not absence of an error at the source. Offline receiver tests reproduce the previous application's `{}` serialization.

The 04:18–04:25 UTC Docker log query returned no entries. A bounded current five-minute sample contained no matching failure. Historical provider, DNS, TLS, deadline, HTTP, parser and failover causes cannot be reconstructed. The user's recurrence report is credible incident input, but retained evidence does not quantify frequency, user impact or provider distribution. No production search was replayed. A broad retained-log scan was rejected by automatic approval review and was not executed or retried.

## Source impact

The search manager previously stringified all provider errors, losing typed causes and exposing provider text. It also attempted fallback engines after the caller context ended, and invoked an engine when the context was already canceled. Behavioral regressions reproduce these defects. Failover now preserves causes for `errors.Is`/`errors.As`, stops work when the caller context ends, and retains existing attempt/distinct-engine limits and successful fallback behavior. Provider instances, priority configuration and credentials are unchanged. A per-provider HTTP client timeout keeps the healthy fallback path available while the caller is active; an actual local HTTP test reproduced and rejected the first candidate that confused this timeout with caller deadline expiry.

Provider diagnostics omit query text, query length, credentials, headers, endpoint URLs, raw response bodies and provider-reported payload messages. Known providers return safe typed status/stage failures, preserving typed transport/read/decode causes internally. HTTP status is checked before reading error bodies. This is an observability and privacy repair; no historical provider outage is asserted or repaired speculatively.

Search events use an application base logger with a reviewed opaque request ID propagated through the search context. They do not inherit the framework request logger: the MCP framework binds client addresses and paths, and other callers can bind arbitrary user fields. JSON-log negative controls demonstrate this leakage before the correction. This deliberate logger choice follows the task's stricter privacy requirement; the repository's general framework-logger convention would retain those user fields.

The MCP success schema remains unchanged. Failures still use the MCP tool-error result rather than a transport error. Search failures expose a safe category, and billing-denial results use a generic message instead of arbitrary upstream text. Accepted billing is not repeated, refunded or otherwise changed by this patch.

## Diagnostic contract

`web_search failed` authors `stage`, `category`, `provider`, `attempts`, optional
`status`, `duration_ms`, and `request_id` once. Production installs the SDK's
sanitized SDK default hook within reviewed operation loggers, without an application field-name allowlist.
Ordinary future diagnostic primitives from unrelated events also remain useful.
Legacy explicit-filter prefix adapters remain optional and retain their selection
behavior when deliberately chosen.

- Stages: configuration, request, transport, response, decode, provider, manager, encode.
- Categories: canceled, deadline, dns, tls, connection, timeout, rate_limit, auth, provider_http, malformed_response, internal, unknown.
- Provider is a reviewed type (google, bing, serp_google, firecrawl), never a configured instance name. Unknown implementations remain unknown.
- Status is 100–599 and only present for an HTTP category. Duration is 0–24 hours in milliseconds. Attempt count is bounded to 0–100; pre-dispatch cancellation reports zero.
- Correlation is a canonical nonzero UUID from the request context, or a newly generated UUID. It reaches manager attempt logs and the final tool event without hashing query/user data.
- Mixed causes report manager/unknown and no misleading status. Individual attempt warnings carry safe stage/category/provider/status and correlation. Unknown errors are not classified by text. A transport deadline with an active caller is classified as provider-local timeout; an ended caller reports canceled/deadline, retaining earlier attempted causes.

Returned search errors and typed cause identity retain their existing contract.
Log presentation separately preserves useful sanitized internal cause text, bounded
to 2048 bytes and 32 cause nodes. Known JSON/parser body-bearing values use type-based descriptions with useful
offset/expected-type context. Routine DNS hostname and transport operation/network
context remain available after sanitization; no blanket stage-based text suppression
is applied.
Known providers continue discarding HTTP/provider error bodies and messages at the
source. Joined failure presentation does not replace honest mixed categories or
statuses. Typed billing diagnostics omit the provider's raw Reason body and retain
only validated outcome and actual bounded HTTP status.

The SDK sanitizes recognizable credentials, URLs and labeled payloads; it cannot
identify arbitrary unlabeled personal text. Sources must never put queries, user
content or raw provider bodies into diagnostic messages. Logger bases exclude
inherited client/framework fields. Optional legacy explicit-filter adapters still
validate prefixes, duplicates, namespaces and types without invoking arbitrary
marshalers; production relies on authored safe diagnostics and SDK sanitization.

## Acceptance and release

Offline tests cover actual provider JSON logs, typed cause propagation, positive successful responses, HTTP auth/429/5xx, malformed responses, DNS/TLS/connection/timeout, cancellation, mixed failover, nil output, safe correlation and local-receiver alert serialization. Negative controls use sensitive canaries and the previous manager/tool implementations. Repository lint and race/coverage acceptance must pass on the exact final commit without changing source/module files.

Qualified source merges require current owner authorization and exact-head checks; runtime rollout remains separately held. Before a separately approved rollout, qualify the exact image built from the reviewed commit, verify the actual startup hook delegates reviewed search/fetch diagnostics to SDK `GetZapHook` without a field-name filter, and exercise a synthetic local alert receiver without paid production search. Confirm a controlled failure carries reviewed scalar fields and no sensitive content. Monitor category/status/attempt distributions and alert delivery; unknowns should trigger further bounded correlation, not speculative labels.

Rollback is a separately approved release of the previously qualified immutable image or a source revert followed by normal image qualification. Preserve the existing fetch verification, redaction, credentials and configuration. A rollback restores the prior search diagnostic gap and raw-provider telemetry risk. Unrelated SSO/Ramjet release holds and frontend/dependency ownership are independent gates.

Startup scope safety: the application exports default diagnostics from its static
`graphql.laisky-graphql.mcp.web_search`, `graphql.laisky-graphql.mcp.web_fetch` and
`graphql.laisky-graphql.mcp.web_fetch.fetch_dynamic_url_content` loggers. Unrelated
services still emit arbitrary user/provider errors, so their alert fields retain
legacy metadata-only export. One dispatcher calls one existing SDK hook per entry;
there is no duplicate alert stream or diagnostic field-name filter. Actual startup
and HTTP receiver controls retain future ordinary fields/useful causes in those
scopes while rejecting user-ID/unlabelled payload fields and scope lookalikes.
