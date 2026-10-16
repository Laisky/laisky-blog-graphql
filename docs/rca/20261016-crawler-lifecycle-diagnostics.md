# Crawler lifecycle diagnostics

The Yahoo task's original renderer cause remains unknown. A legacy `canceled` reason, an approximately seventeen-second lifetime, and a successful audit write do not establish user cancellation, a deadline, a challenge, an outage, terminal publication, or caller receipt. These changes address reproduced observability gaps without changing fetching, verification, queue, billing, or response behavior.

## Correlation and evidence boundaries

The existing canonical task UUID joins the worker to the caller. The caller's reviewed logger supplies its request UUID, allowing the prepared response to join the same operation. The queue has no distributed request/trace or cancellation envelope; no synthetic trace identity or new envelope field is introduced. Context state is sampled at the logging boundary and does not prove the context caused the error.

| Event | What it establishes | What it does not establish |
| --- | --- | --- |
| `processing_completed` | Worker processing ended with the recorded success/failed status | Result publication or caller receipt |
| `result_published` | The terminal Redis publication call returned success | Durable replication, consumer observation, or client receipt |
| `result_publication_failed` | Running/terminal publication failed; `cleanup` distinguishes bounded cleanup | Successful terminal publication |
| `audit_persistence_failed` | Nonfatal audit persistence failed | Failure of the separate terminal publication |
| `result_observed` | Caller read a result/status transition under the submitted task key | Authenticity or acceptance of its content |
| `result_verified` | Caller accepted a verified, bounded, nonempty result | Successful response construction or client receipt |
| `response_prepared` | MCP tool JSON construction succeeded | Transport delivery or client acknowledgment |

Worker events use canonical task IDs, finite result/publication outcomes, `worker_context_state` and elapsed `duration_ms` bounded to five minutes. Caller milestones use the request/task join, finite status, `caller_context_state` and duration bounded to twenty-four hours. INFO result observation is transition-only, not emitted for each poll. The legacy explicit alert adapter is unchanged; production's default SDK path carries newly authored safe fields.

`caller_context_state` and `worker_context_state` are `active`, `canceled`, or `deadline_exceeded` and refer to different contexts. The worker runs on an independent background deadline; its cancellation must not be called caller cancellation. Caller `error_lifecycle` describes the bounded error graph: `none`, `canceled`, `deadline_exceeded`, `mixed`, or `unknown`. Mixed includes concrete plus lifecycle causes; unknown covers the traversal cap. Existing error codes and error identities remain compatible and do not by themselves establish causality.

Optional worker `upstream_status` is retained only after an actual document HTTP response and a subsequent exchange failure. Subresource status is excluded. An observed status does not prove it caused the failure; ordinary non-2xx handling remains unchanged. No status is invented when no response was observed.

Only reviewed scalar fields and sanitized errors are authored. No query, URL, credentials, authorization headers, article bodies, tenant identity, or raw upstream response is added to diagnostics. No blanket output filter, redaction weakening, DTO, dependency, retention, frontend, or configuration change is introduced. Already-emitted worker errors retain their identity and skip duplicate outer emission while existing backoff remains.

## Behavioral qualification

Compile-valid RED tests on unchanged implementation reproduce missing context/lifecycle fields in actual JSON and SDK HTTP alerts, missing processing/publication milestones, and lost document status after malformed gzip. GREEN tests retain privacy canaries and negative controls: active caller with dependency cancellation/deadline, mixed/wrapped errors, bounded traversal, invalid enums, verification rejection after observed success, running/terminal/cleanup publication failures, nonfatal audit failure, ordinary HTTP 403 acceptance, and subresource status exclusion.

The paired `TestMCPWebFetchServicePath` uses actual MCP/billing/alert HTTP, disposable Redis and a separate worker test process. It joins exact task/request IDs across local worker processing/publication logs, caller observation/verification/preparation, encoded results and actual SDK HTTP diagnostics. Only local ordering is asserted: after Redis SET, consumer observation and the producer's subsequent log can race across processes. Billing, egress and content-release negative controls remain intact.

The fixture replaces Chrome and public-peer observation and records call-log persistence. Status propagation is exercised through a renderer seam, not a real browser callback. Direct worker lifecycle tests do not execute the outer infinite scheduling loop; transparent marker/error identity and unchanged backoff code are reviewed. Fixture alert delivery is not production receiver/Telegram receipt. No live target or paid operation is used. The exact heads, commands, exits and current acceptance results belong in the Draft descriptions and retained receipts.

## Acceptance and release boundary

Run Go 1.27.2 named caller/worker regressions with race, the paired race fixture, uncapped scoped lint/vet, goimports/whitespace and the GraphQL essential-unit gate under the shared validation lease. Preserve source/module/frontend bytes during acceptance. Retain failing RED and correction receipts; a skipped optional worker fixture alone is not paired acceptance.

This follow-up is a source candidate in the existing Drafts. It is not the already deployed combined source `697509c103835991d4c2bf71f033038450730193` / `3d2c5dc0cae3f26c1600f0f23701e2b1cc26f147`. No merge or deployment is part of this source update. Future activation requires separately qualified integration with the owned frontend/module changes, immutable image/source binding, fresh runtime guards and rollback acceptance. Existing SSO/Ramjet holds remain.

The unchanged accepted runtime images provide the rollback baseline: GraphQL `ppcelery/laisky-blog-graphql@sha256:3b6ed2c8c280c3fa01f6037cb578c23cee227ad59456a51591fcfafb1e356765`, worker `ppcelery/go-ramjet@sha256:a5cd9e56d6c8e89ef91e893b25d8db7e7a7959433fdd78f935e80d6891baf119`. A future failed acceptance rolls producer back before worker through the existing reviewed release procedure; no queue migration or security/configuration change is required by this diagnostic-only update.

Production retained-result access remains capped at the already authorized first 256 bytes. The separately corrected local collector cannot reach metadata after a truncated value and must report unknown. No larger read or denied-route retry is authorized by these source improvements. Quiet evidence, generic success, and synthetic qualification do not establish Yahoo recovery.
