# Crawler egress policy

Scope: the `web_fetch` path shared by the MCP tool, the GraphQL `WebFetch`
mutation and the browser console. This document is the contract between this
server, which admits a fetch, and the renderer, which performs it.

## Why admission alone is not enough

Request admission runs here. It rejects non-HTTP(S) schemes, credentials in the
authority, local and non-canonical numeric hosts, and any hostname that resolves
to a non-public address. That decision is made from *this* server's DNS answer at
*that* moment.

The connection, however, happens on a separate renderer host. Four gaps follow
directly from that split, and none of them can be closed by a DNS lookup here:

1. **DNS rebinding.** By the time the renderer resolves the hostname again, the
   authoritative answer can point at a private address. The name that passed
   admission and the address that gets connected to are not the same fact.
2. **Redirects.** A target that passes admission can answer `302` toward a
   private address or another origin. Each hop is a new request that was never
   admitted.
3. **Subresources.** A rendered document pulls scripts, images, stylesheets and
   frames. Each is another request from inside the renderer.
4. **Split-horizon answers.** A zone can return one public and one private
   address for the same name.

## The contract

`toolpolicy.AdmitFetchURL` returns an `Admission` that carries the exact
addresses it validated, not just a verdict. `Admission.Policy` converts that into
an `EgressPolicy`, which travels with the crawl task as
`HTMLCrawlerTask.Egress` and is published to GraphQL workers as
`GeneralHTMLCrawlerTask.egress`:

| Field | Meaning for the renderer |
| --- | --- |
| `host` | The only hostname this task may resolve. |
| `addresses` | The only addresses it may connect to for `host`. Do not resolve the name again. |
| `max_redirects` | Hops allowed after the initial request. `0` means follow none. |
| `allow_subresources` | When false, fetch only the document itself. |

A renderer that receives **no** policy must refuse the crawl. A missing policy
means the task was queued without admission metadata; it is not permission.

Every origin the renderer contacts — the initial request and each hop — must be
reported back in `HTMLCrawlerTask.RequestChain`, oldest first.

## Server-side verification

`toolpolicy.VerifyEgressChain` re-admits every reported hop before any body
reaches the caller:

- A hop that fails admission on its own (a private address, a local hostname, a
  non-canonical numeric host) fails the fetch.
- A hop on the admitted `host` that resolves outside `addresses` fails the fetch.
  This is the rebinding case.
- More hops than `max_redirects` fails the fetch.
- A cross-origin hop is accepted only when it independently passes admission;
  the pinned address set belongs to the original host.

`ErrEgressUnverified` is deliberately a distinct condition from a violation. An
empty chain is not proof of safety and not proof of a breach — it means the
renderer did not report, so nothing was checked.

## Configuration

| Key | Default | Effect |
| --- | --- | --- |
| `settings.mcp.tools.web_fetch.egress.max_redirects` | `5` | Redirect budget published to the renderer and enforced on the reported chain. |
| `settings.mcp.tools.web_fetch.egress.allow_subresources` | `false` | Whether the renderer may load page subresources. |
| `settings.mcp.tools.web_fetch.egress.require_verified` | `false` | When true, a render result with no reported chain is rejected. |

`require_verified` defaults to false so an existing renderer keeps working: the
policy is published and any reported chain is verified, but an unreported crawl
is logged as unverified rather than failed. **Enable it once the renderer
reports its chain.** Until then, an unreported crawl is exactly that —
unverified — and this document does not claim otherwise.

## What is verified and what is not

Verified in this repository, with behavior tests:

- The admitted address set is computed and published (`internal/library/toolpolicy/egress_test.go`).
- A partially private DNS answer rejects the whole name.
- The policy survives the Redis round trip and a task without admission
  metadata decodes to an explicitly absent policy, not a permissive one
  (`library/search/egress_test.go`).
- A reported chain that leaves the admitted origin, rebinds the host, or exceeds
  the budget fails closed before any body is returned.
- An unreported chain is classified as unverified, and `require_verified`
  rejects it.

**Not verified here:** the renderer itself. This repository does not contain the
crawler, so nothing in it can prove that the renderer pins its sockets, bounds
its redirects, or restricts subresources. The server-side half fails closed on
what the renderer reports; enforcing the policy inside the renderer, and
reporting the chain truthfully, is the renderer's obligation. A renderer that
reports a clean chain while connecting elsewhere is not detected by this
mechanism.
