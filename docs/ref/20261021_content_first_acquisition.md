# Content-first crawler result compatibility

Task: 20261013-02; companion to [go-ramjet issue 94](https://github.com/Laisky/go-ramjet/issues/94).

## Problem and minimal correction

The worker's existing provider adapters cannot restore MCP retrieval by themselves.
The GraphQL caller required every success to carry a direct target receipt, so
honestly labeled provider content was rejected as `crawler_egress_unverified`.
The queue producer also had no capability field permitting a newer worker to
return that alternative result to a compatible caller.

Add `allow_provider_fallback` and `acquisition` to the existing JSON task envelope.
The shared verifier keeps the direct receipt path intact and accepts provider
provenance only when the producer advertised protocol support, for a public task
without a fabricated direct receipt. Effective worker provider configuration,
including existing defaults, permits forwarding the whole eligible target URL;
the capability is not per-request user consent. Direct-only producers retain a
missing or false capability.
All existing task identity, policy, output and result limits remain enforced.
Public byte-returning APIs, the MCP `content` schema, billing and queue keys do
not change. The worker implementation remains in the companion Ramjet PR.

## Behavioral reproduction and regression

Base: `a5b4b7c6f77388594e2ce183c9a4fc000d706a87`.
Regression-only commit:
[`f55ca75`](https://github.com/Laisky/laisky-blog-graphql/commit/f55ca7524c04e9000185d187c790264f1d20290d).
Its tree is identical to the local snapshot used for the original RED run.

The first command below failed before implementation: both provider output modes
were rejected with `crawler_egress_unverified`, and the producer assertion found
no `allow_provider_fallback` field. The same command passed after the fix. The
test injects additive fields through the real JSON task decoder, so the RED is
a runtime behavior failure rather than a compilation failure on a missing type.

```sh
go test -count=1 ./library/search ./library/db/redis \
  -run 'TestProviderFallbackResultReachesCaller|TestCrawlerProducerAdvertisesProviderFallback' \
  -v -timeout=90s

go test -count=1 ./library/crawleregress ./library/search ./library/db/redis -timeout=90s
```

Initial qualification used Go 1.27.2 and disposable local Redis fixtures. Final
standalone and paired-process acceptance was repeated with both services built
on Go 1.27.3 after the build-security follow-up. Permanent
regressions cover direct legacy receipts, provider capability escalation,
task/target/policy/output tampering, missing or contradictory provenance,
provider-name syntax, cancellation, and all four old/new producer-worker JSON
round trips. Provider lifecycle events identify acceptance without labeling it
direct connection verification. These tests do not establish production recovery.

### Cross-process service acceptance

`TestMCPWebFetchServicePath` now retains its original eight queued cases and
adds four provider cases. Every case traverses actual MCP, billing and alert
HTTP sockets, owned Redis 8.10.2 over a disposable Unix socket, a separate
compiled Ramjet worker process, result publication, GraphQL verification and
the MCP response. The existing three billing/auth/admission cases remain.

| Added case | Direct observation | Provider behavior | Required caller outcome |
| --- | --- | --- | --- |
| `provider-challenge` | HTTP 403 with an active challenge element | Existing prefix adapter returns Markdown | Original Markdown reaches MCP; provider provenance, no direct receipt or chain |
| `provider-timeout` | Typed header timeout with an active worker context | Same adapter returns Markdown | Locally rendered HTML reaches MCP with provider provenance |
| `provider-error` | HTTP 403 with active challenge element | Owned provider responds HTTP 503 | Ordinary crawler failure; no provider payload in response, logs or alerts |
| `forged-provider-result` | HTTP 403 with active challenge element | Worker succeeds; fixture adds a fabricated direct URL chain after processing | Caller rejects the result without releasing content |

The HTTP adapter uses the production scoped policy transport. Its resolver and
dialer are pinned test dependencies that connect only to local HTTP fixtures;
their public peer addresses are synthetic. The timeout is a deterministic
`net.Error`, not an assertion about the timing or cause of any historical incident.
Chrome and call-log SQL persistence remain the existing fixture seams.

The test also checks actual provider metadata in both stored crawl-record
indexes, exact task/request IDs, retained billing outcomes, three-hour result
retention, independent direct receipt failures and unchanged legacy queue data.
Provider success emits `result_accepted`, with method/provider fields, while
direct success retains `result_verified` and its existing log message. A
prepared response is not labeled as proof that a remote client received it.

The old blanket log/alert exclusion of the selected original URL/HTML predated
the shipped original-input capture contract. The fixture now positively checks
those exact selected inputs in existing worker and MCP failure events and actual
SDK HTTP alerts. Credentials, billing response bodies, successful provider
content and failed provider response bodies remain excluded.

With a local Go workspace selecting both companion source trees and
`redis-server` on `PATH`, run:

```sh
# In go-ramjet:
go test -race -c -o /tmp/issue94-ramjet-service-race.test ./internal/tasks/gptchat/tasks

# In laisky-blog-graphql:
CRAWLER_SERVICE_TEST_WORKER_BINARY=/tmp/issue94-ramjet-service-race.test \
  go test -race -count=1 -timeout=3m ./internal/mcp \
  -run '^TestMCPWebFetchServicePath$' -v

go test -race -count=1 ./library/crawleregress ./library/search ./library/db/redis -timeout=90s
go test -race -count=1 ./internal/mcp/tools -run 'TestWebFetch' -timeout=90s
go vet ./library/crawleregress ./library/search ./library/db/redis
```

The initial complete socket run passed all 15 subcases in 12.204 seconds with
zero skips. Final acceptance, including both persisted provenance indexes, also
passed all 15 with the race detector in both processes (13.469 seconds for the
GraphQL package). The three complete shared-package race suites, targeted MCP
`TestWebFetch` race suite, targeted vet and read-only goimports checks passed.
Separate pre-fix worker and GraphQL caller behavioral failures are
the RED evidence; this expanded service fixture was not run against an old
worker binary. Missing helper binaries or Redis are incomplete acceptance,
not successful execution of this service contract.

## HOWTO: rollout and rollback

Review the paired worker and caller changes and qualify their full MCP-to-worker
path before an eligible release. Repository release pushes can automatically
deploy; creating this source change does not authorize a deployment. Either
source can coexist with the receipt-capable old counterpart for direct retrieval;
provider recovery requires both new versions. Preserve queue keys and pending
tasks. Drain accepted work before rolling back the capable caller so an in-flight
provider result is not handed to an old direct-only decoder. Restore the prior
immutable image if release acceptance fails. No schema migration, credential,
runtime configuration or stored-data rewrite is introduced.

## Primary references checked on 2026-10-21

- [Go context](https://pkg.go.dev/context): propagate the existing context through
  the call chain; a canceled context and its derived contexts must stop work.
- [Go HTTP requests](https://pkg.go.dev/net/http#NewRequestWithContext): an outgoing
  request's context governs connection establishment, headers and response-body
  reading. A fallback must use the remaining context, not a new background budget.
- [Go JSON decoding](https://pkg.go.dev/encoding/json#Unmarshal): test additive
  struct fields through actual old/new decoding. Capability defaults must remain
  false when old producers omit the field.

These references support the implementation choices; they do not prove remote
provider routing or immediate cancellation of a separate queued worker. The
current queue contract has no distributed cancellation field.
