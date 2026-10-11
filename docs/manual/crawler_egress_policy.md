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
Unsupported requests fail closed before an upstream connection. No browser
proxy from the environment, credential-bearing external fetch service, LLM
conversion fallback or unverified legacy cache is used in this task path.
Markdown conversion is local and needs no provider key.

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

Before releasing any body, `crawleregress.VerifyReceipt` checks:

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
Neither condition releases content under the secure default.

Receipts are audit evidence from a trusted renderer. They are not signed
attestations and cannot establish truth against a compromised worker that
fabricates them. Source and artifact verification, worker access control and
renderer behavior tests remain part of acceptance.

## Configuration and compatibility

| Key | Default | Effect |
| --- | --- | --- |
| `settings.mcp.tools.web_fetch.egress.max_redirects` | `5` | Document redirect budget published to the renderer. |
| `settings.mcp.tools.web_fetch.egress.allow_subresources` | `false` | Whether public page subresources may be fetched. |
| `settings.mcp.tools.web_fetch.egress.require_verified` | `true` | Require connection receipts before releasing content. |

The secure default is true when its key is absent. This repair does not select
an unsafe compatibility override. The existing explicit-false compatibility
option is unsafe and does not authorize a mismatched task/policy or reported
violation. An old worker model that drops egress fields cannot satisfy this
contract; pin the same shared module revision in the worker.

## Diagnostics and billing

The MCP failure alert exports reviewed scalar `error_code`, `stage`, UUID
`request_id`/`task_id` and bounded `duration` for this exact callsite. It does not
export URLs, raw errors, credentials, configuration or page content. Other
alerts retain their existing restrictive policy. Local structured diagnostics
remain available; outward failure text uses safe codes.

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

Rollback must preserve the producer/worker contract. Reverting only to an old
worker restores unavailable fetches under the secure verifier; weakening the
verifier is not a rollback strategy. Preserve previous image digests, drain or
expire incompatible queued tasks using a separately reviewed operational plan,
and report readiness before any production-changing step. Draft PR status is
retained until the recorded acceptance gates pass.

## API references

The implementation uses the documented [Go HTTP transport](https://pkg.go.dev/net/http#Transport),
[Go TLS verification](https://pkg.go.dev/crypto/tls#Config),
[CDP request fulfillment](https://chromedevtools.github.io/devtools-protocol/tot/Fetch/#method-fulfillRequest),
and [Chromium proxy behavior](https://chromium.googlesource.com/chromium/src/+/HEAD/net/docs/proxy.md).
These API references describe mechanisms; the candidate's fixture tests establish
its behavior and must pass before production readiness is claimed.
