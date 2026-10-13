# Web search failure diagnostics

## Incident evidence

Task20261013-02 investigated `graphql.laisky-graphql.mcp.web_search`, `web_search failed`, at `2026-10-13T04:21:50.160442029Z`, whose alert fields were `{}`.

The running production image index is `sha256:6bdd547f6c727a702475dc96ba980bb7ada62ee3df31e64a41454ff617a10762`. Its linux/amd64 manifest config matches the running container image `sha256:4bc81ba89f52175e0b48eed55aa3c055df212cbddc29253887088fb2f0663054`. Public BuildKit provenance and [the workflow run](https://github.com/Laisky/laisky-blog-graphql/actions/runs/38112178357) identify source `ff2a61a5543f579ce738e58f2bedf1b201ac7414`. Provenance was read and matched; signature authenticity was not independently verified. Binary metadata identifies go-utils/v6 `v6.3.2-0.20261008165128-506ec9758d5f`.

At that exact source, the search error callsite supplies `zap.Error(err)` and `query_len`. The installed `WebFetchAlertHook` forwards nil fields for all callsites except the approved fetch event. Therefore this search alert's empty payload is explained by downstream filtering, not absence of an error at the source. Offline receiver tests reproduce the previous application's `{}` serialization.

The 04:18–04:25 UTC Docker log query returned no entries. A bounded current five-minute sample contained no matching failure. Historical provider, DNS, TLS, deadline, HTTP, parser and failover causes cannot be reconstructed. The user's recurrence report is credible incident input, but retained evidence does not quantify frequency, user impact or provider distribution. No production search was replayed. A broad retained-log scan was rejected by automatic approval review and was not executed or retried.

## Source impact

The search manager previously stringified all provider errors, losing typed causes and exposing provider text. It also attempted fallback engines after the caller context ended, and invoked an engine when the context was already canceled. Behavioral regressions reproduce these defects. Failover now preserves causes for `errors.Is`/`errors.As`, stops work when the caller context ends, and retains existing attempt/distinct-engine limits and successful fallback behavior. Provider instances, priority configuration and credentials are unchanged. A per-provider HTTP client timeout keeps the healthy fallback path available while the caller is active; an actual local HTTP test reproduced and rejected the first candidate that confused this timeout with caller deadline expiry.

Provider diagnostics omit query text, query length, credentials, headers, endpoint URLs, raw response bodies and provider error messages. Known providers return safe typed status/stage failures, preserving typed transport/read/decode causes internally. HTTP status is checked before reading error bodies. This is an observability and privacy repair; no historical provider outage is asserted or repaired speculatively.

The MCP success schema remains unchanged. Failures still use the MCP tool-error result rather than a transport error. Search failures expose a safe category, and billing-denial results use a generic message instead of arbitrary upstream text. Accepted billing is not repeated, refunded or otherwise changed by this patch.

## Diagnostic contract

`web_search failed` uses dedicated local `web_search_*` fields. The alert adapter validates these fields and emits `stage`, `category`, `provider`, `attempts`, optional `status`, `duration_ms`, and `request_id`.

- Stages: configuration, request, transport, response, decode, provider, manager, encode.
- Categories: canceled, deadline, dns, tls, connection, timeout, rate_limit, auth, provider_http, malformed_response, internal, unknown.
- Provider is a reviewed type (google, bing, serp_google, firecrawl), never a configured instance name. Unknown implementations remain unknown.
- Status is 100–599 and only present for an HTTP category. Duration is 0–24 hours in milliseconds. Attempt count is bounded to 0–100; pre-dispatch cancellation reports zero.
- Correlation is a canonical nonzero UUID from the request context, or a newly generated UUID. It reaches manager attempt logs and the final tool event without hashing query/user data.
- Mixed causes report manager/unknown and no misleading status. Individual attempt warnings carry safe stage/category/provider/status and correlation. Unknown errors are not classified by text. A transport deadline with an active caller is classified as provider-local timeout; an ended caller reports canceled/deadline, retaining earlier attempted causes.

The existing fetch adapter and unrelated metadata-only alert behavior remain in place. Generic field collisions, duplicates, namespaces, invalid types, oversized strings and arbitrary marshalers do not open a new export channel.

## Acceptance and release

Offline tests cover actual provider JSON logs, typed cause propagation, positive successful responses, HTTP auth/429/5xx, malformed responses, DNS/TLS/connection/timeout, cancellation, mixed failover, nil output, safe correlation and local-receiver alert serialization. Negative controls use sensitive canaries and the previous manager/tool implementations. Repository lint and race/coverage acceptance must pass on the exact final commit without changing source/module files.

This task does not authorize deployment or merging. Before a separately approved rollout, qualify the exact image built from the reviewed commit, verify it uses `ToolFailureAlertOption` with `ToolFailureAlertHook`, and exercise a synthetic local alert receiver without paid production search. Confirm a controlled failure carries reviewed scalar fields and no sensitive content. Monitor category/status/attempt distributions and alert delivery; unknowns should trigger further bounded correlation, not speculative labels.

Rollback is a separately approved release of the previously qualified immutable image or a source revert followed by normal image qualification. Preserve the existing fetch verification, redaction, credentials and configuration. A rollback restores the prior search diagnostic gap and raw-provider telemetry risk. Unrelated SSO/Ramjet release holds and frontend/dependency ownership are independent gates.
