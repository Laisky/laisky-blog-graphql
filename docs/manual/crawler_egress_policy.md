# Crawler egress policy

Scope: MCP `web_fetch`, GraphQL `WebFetch`, the browser console and HTML crawler
queue producers. This is the producer/renderer contract. Incident evidence is
in [20261010-02](../rca/web_fetch_20261010_02.md).

## Admission and connection enforcement

`crawleregress.AdmitURL` rejects non-HTTP(S) schemes, authority credentials,
local/noncanonical numeric hosts, and any DNS answer containing a nonpublic
address. Each lookup has a ten-second child deadline respecting the caller's
earlier deadline and cancellation. The returned admission contains the exact
public addresses, rather than only a URL verdict.

A task carries the admission as `HTMLCrawlerTask.Egress`; GraphQL dequeue
publishes it as `GeneralHTMLCrawlerTask.egress`:

| Field | Meaning |
| --- | --- |
| `host` | Original admitted hostname, canonically normalized. |
| `addresses` | Only addresses the renderer may connect to for this hostname. It must not resolve the original hostname again. |
| `max_redirects` | Maximum document hops after the initial request; zero follows none. |
| `allow_subresources` | Whether independently admitted HTTP(S) page resources may be fetched. |

Missing or invalid policy fails before rendering. A cross-origin destination
needs independent public-address admission. Its validated addresses are then
pinned for the rest of the task; mixed public/private answers reject the name.

The go-ramjet secure renderer intercepts browser HTTP(S) requests and executes
each exchange with a Go transport that dials admitted literal IPs. It preserves
the original HTTP host and normal TLS hostname/CA verification. It does not
follow HTTP redirects itself; each browser hop is independently checked.
Browser paths outside interception go through a task-local deny proxy.
Unsupported requests fail closed before an upstream connection. The direct
renderer uses no browser proxy from the environment, caller credentials, LLM
conversion or unverified legacy cache. Direct Markdown conversion is local.
Eligible direct failures may use configured content providers under the explicit
acquisition capability described below; provider content never claims direct
target connection evidence.

The renderer bounds request count, response headers, individual decoded
responses, aggregate response bytes, render duration and returned content.
These are application work limits, not a claim that page JavaScript/DOM memory
is fully bounded or that the browser process is a hostile-code sandbox.

## Result evidence and verification

A URL copied from the input is insufficient. `HTMLCrawlerTask.EgressReceipt`
contains versioned evidence bound to the exact task ID, exact submitted target
and a digest of the submitted policy. Each actual HTTP connection records its
URL, request kind, admitted address set and actual connected public peer.
`RequestChain` remains a legacy audit field; it cannot substitute for this
receipt in the secure result path.

Before releasing a direct result, `crawleregress.VerifyReceipt` checks:

- Supported version and exact task/target/policy binding.
- Initial document identity, allowing ordinary browser URL normalization
  without changing the submitted target/digest binding.
- Document redirect budget and the subresource decision.
- Public admitted address sets and an actual peer within each set.
- Original-host peers and admissions within the producer's original pins.
- Bounded evidence size and preserved caller cancellation.

Verification uses the supplied validated pins, not a later DNS answer. A safe
pinned connection remains valid even if DNS changes after admission. Missing
receipts are unverified; malformed/mismatched/private-peer evidence is rejected.
Neither condition releases direct content.

Receipts are audit evidence from a trusted renderer. They are not signed
attestations and cannot establish truth against a compromised worker that
fabricates them. Source and artifact verification, worker access control and
renderer behavior tests remain part of acceptance.

### Content-first provider acquisition

`HTMLCrawlerTask.allow_provider_fallback` is an additive producer capability.
Missing or false retains the direct-only contract. New Redis producers advertise
true, and a compatible worker tries direct retrieval before any eligible configured
provider. It reports `acquisition.method` as `direct` or `provider`, with the
actual bounded provider identifier in `acquisition.provider` for provider results.
Provider identifiers are validated for syntax; the shared contract does not
hard-code a provider-name list. Configuration and selection stay in the worker.

`crawleregress.VerifyAcquisition` retains receipt verification for absent metadata
and explicit direct results. A provider result requires the producer's original
permission, valid public target policy, an echoed capability, an empty direct
`RequestChain`, and no `EgressReceipt`. Task ID, exact target, output mode, policy
digest, size limits and nonempty requested output remain checked before release.
Returned metadata cannot opt a direct-only caller into accepting provider output.

This is trusted-worker provenance, not evidence of the provider's downstream
connection route. Provider responses cannot establish that the provider used the
original admitted target pins or followed the same redirects. The worker must
preserve target eligibility, deny fallback after policy or connection-evidence
failures, bound its own provider transport, and reject unusable provider responses.
It must not forward caller API keys, cookies or session credentials. These worker
behaviors are qualified in the companion go-ramjet correction for issue 94.

The existing MCP JSON `content` field and public byte-returning fetch API remain
unchanged. Direct lifecycle success retains `result_verified`. Provider acceptance
emits `result_accepted`, `acquisition_method=provider` and the actual provider name;
it does not emit a direct-verification claim. For HTML output the worker locally
renders provider Markdown into HTML; provider Markdown mode retains its text.

| Producer | Worker | Result contract |
| --- | --- | --- |
| Old | Old | Existing direct receipt. |
| Old | New | Direct receipt; missing capability prevents provider use. |
| New | Old | Direct receipt; the old decoder ignores the additive capability. |
| New | New | Direct receipt or explicitly identified provider content. |

No queue migration or data rewrite is required. A serialized compatibility
regression covers all four pairs. Old callers still reject provider results if
such a result reaches them during an improper rollback. Drain accepted work before
rolling back a capable caller; preserve a compatible worker for pending tasks.
Direct retrieval remains available during a mixed-version rollout, while provider
recovery requires both the capable caller and worker.

Caller cancellation still ends waiting and result acceptance. The existing task
wire has no distributed deadline/cancellation signal, so an already dequeued
worker uses its independent bounded context. Fallback must share that context;
this change does not claim immediate cross-process cancellation.

## Configuration and compatibility

| Key | Default | Effect |
| --- | --- | --- |
| `settings.mcp.tools.web_fetch.egress.max_redirects` | `5` | Document redirect budget published to the renderer. |
| `settings.mcp.tools.web_fetch.egress.allow_subresources` | `false` | Whether public page subresources may be fetched. |
| `settings.mcp.tools.web_fetch.egress.require_verified` | Direct receipts required | Deprecated and ignored, including an explicit `false`; it cannot enable provider fallback. |

Final task identity, exact target, echoed policy and acquisition checks are
unconditional. Missing or empty receipts never release a direct body,
even when the deprecated configuration key is explicitly false or a source
caller constructs `EgressSettings{RequireVerified: false}`. `LoadEgressSettings`
always reports `RequireVerified: true`; the field remains for source
compatibility and cannot weaken enforcement. An old worker model that drops
egress fields cannot satisfy this contract. The mandatory-verification
follow-up changes no task/receipt wire or queue generation, so a receipt-capable
worker already pinned to the verified-v1 contract remains compatible.

Fresh-default standalone probes attest the exact consumer and defaults, not a
running service's effective redirect/subresource configuration. Mandatory
receipt enforcement itself is a property of the qualified source/artifact and
does not depend on inspecting runtime settings or credentials.

### Queue generation and mixed fleets

`KeyTaskHTMLCrawlerPending` routes every current producer and consumer to
`laisky/tasks/html_crawler/verified/v1/pending`. This queue generation belongs
to the pinned-policy and connection-receipt v1 contract. Already compiled
legacy workers continue using `laisky/tasks/html_crawler/pending`, exposed as
`KeyTaskHTMLCrawlerLegacyPending` only to identify that retained queue. Current
workers never consume both queues and producers never fall back to the old key.
Do not migrate, delete or drain existing legacy tasks as part of this release.
Task UUIDs and `laisky/tasks/html_crawler/result/` remain unchanged; a result
still needs exact submitted identity, policy and acquisition verification.

A worker upgrade alone cannot isolate tasks on a shared queue. The October 11
owned fixture produced a legacy result despite deployment of a qualified new
worker, exposing competing legacy consumers beyond that container. The
versioned key prevents those old binaries from receiving new verified tasks
without credential changes or relaxed verification. This isolates accidental
routing by consumers with the literal old Redis key; it is not an ACL or
capability boundary. The existing authenticated GraphQL dequeue API intentionally
uses the current contract. A client authorized to call that API or arbitrary
Redis commands is not excluded by queue naming; the receipt verifier still
fails closed on every returned result.

## Diagnostics and billing

The MCP failure alert exports reviewed scalar `error_code`, `stage`, UUID
`request_id`/`task_id` and bounded `duration_ms` for this exact callsite. Failed
worker results may additionally carry optional JSON strings `failure_stage`
and `failure_category`. The producer exports these as `worker_stage` and
`worker_category` only when failed status, task UUID, exact target, output mode
and echoed policy match the submitted operation. These fields describe the
worker's failure stage; they are diagnostic metadata, not receipt or
authentication proof. They never release a body or weaken success verification.

The existing consumer `crawler_failed`/`render` code and stage remain unchanged.
Missing, partial, unknown, invalid or mismatched worker metadata leaves the
generic failure. Old decoders ignore the additive keys and new decoders accept
old payloads. Legacy `failed_reason` remains compatible but is never parsed or
exported as diagnostics. Worker `canceled`/`timeout` categories do not imply the
producer's caller context was canceled or expired.

Reviewed worker stage/category pairs are:

| Worker stage | Categories |
| --- | --- |
| `admission` | `policy_missing`, `policy_invalid`, `unsupported_proxy`, `task_missing` |
| `render` | `render_failed`, `challenge`, `empty_body`, `result_too_large` |
| `verification` | `evidence_rejected` |
| `conversion` | `conversion_failed`, `empty_body`, `empty_markdown`, `result_too_large` |

Each stage also permits `canceled`, `timeout` and `worker_failed`. The category
must match its stage exactly; freeform text is never classified by substring.

Alerts and local fetch logs exclude target URLs, inherited framework/client
fields, raw error causes, credentials, configuration and page content. A reviewed
application logger carries UUID correlation to the fetch state machine; returned
errors still preserve internal typed causes while log presentation includes bounded
sanitized useful cause text. Typed billing response bodies are excluded entirely;
only validated billing outcome and HTTP status enter logs. Production installs
the SDK's default hook within the reviewed search/fetch loggers, without a field-name allowlist. Authored fetch fields
use the existing generic alert names once, and the SDK sanitizes diagnostic
primitives, credentials, URLs and labeled payloads. Optional legacy explicit-filter
adapters retain their documented selection behavior when deliberately chosen.

Admission/authorization rejection precedes paid acceptance. An accepted fetch
that later fails keeps existing billing behavior. The incident does not justify
changing charging/refund policy without a separate product decision.

## Coordinated acceptance and release

Both repositories require red-to-green behavioral regressions and valid-public
positive controls. Acceptance covers missing/forged/mismatched receipts,
pinned destinations, rebinding, private redirects/resources, limits,
cancellation/timeouts, conversion/proxy fallback isolation, browser bypass
paths, billing and alert privacy. Run required repository checks and real Chrome
fixture tests without authenticated fetches or production request replay.

Release only digest-pinned candidates tied to tested commit SHAs. Build and
validate the receipt-capable renderer against the reviewed shared module first,
then validate the producer and renderer together. Pending/old tasks lacking
policy must fail safely; do not make them appear verified. A rollout must keep
verification enabled and use bounded synthetic public fixtures plus reviewed
scalar diagnostics to assess outcomes.

Under the automatic-latest delivery policy adopted on 2026-10-17, eligible
release pushes publish images and automatically deploy `latest` after a successful
image build. `[publish only]` no longer suppresses deployment. Complete the required
source and compatibility qualification before an eligible merge or push; then
record the exact published digests and actual runtime identities. A later rebuild
may contain a different Chrome release. Coordinate the worker-first rollout below
before releasing a producer change that depends on the new worker contract.
Deploy the receipt-capable worker pinned to the versioned queue first. Before
deploying the producer, submit an owned fixture with the same shared module
and verify its UUID in that worker's artifact-qualified logs, the complete
returned policy/receipt and the exact consumer's HTML and Markdown controls.
A success status or body alone is insufficient. Retain mixed-fleet tests in
both repositories proving that legacy consumers cannot receive verified tasks
and current consumers leave old tasks untouched. Preserve queued tasks and
configuration; no queue fallback, dual consumption or migration is permitted.

Rollback must preserve both queue generation and producer/worker contract.
Reverting only the worker or producer to a pre-generation binary can leave
accepted work without a compatible consumer. Preserve both pending queues
and prior image digests, and keep a compatible verified consumer for accepted
verified tasks. Weakening verification or moving work to the legacy queue is
not a rollback strategy. Report readiness before any production-changing step. Draft PR status is
retained until the recorded acceptance gates pass.

## API references

The implementation uses the documented [Go HTTP transport](https://pkg.go.dev/net/http#Transport),
[Go TLS verification](https://pkg.go.dev/crypto/tls#Config),
[CDP request fulfillment](https://chromedevtools.github.io/devtools-protocol/tot/Fetch/#method-fulfillRequest),
and [Chromium proxy behavior](https://chromium.googlesource.com/chromium/src/+/HEAD/net/docs/proxy.md).
These API references describe mechanisms; the candidate's fixture tests establish
its behavior and must pass before production readiness is claimed.

Startup scope safety: the application exports default diagnostics from its static
`graphql.laisky-graphql.mcp.web_search`, `graphql.laisky-graphql.mcp.web_fetch` and
`graphql.laisky-graphql.mcp.web_fetch.fetch_dynamic_url_content` loggers. Unrelated
services still emit arbitrary user/provider errors, so their alert fields retain
legacy metadata-only export. One dispatcher calls one existing SDK hook per entry;
there is no duplicate alert stream or diagnostic field-name filter. Actual startup
and HTTP receiver controls retain future ordinary fields/useful causes in those
scopes while rejecting user-ID/unlabelled payload fields and scope lookalikes.
