# Opt-in Blog SSO code handoff

This is a scoped custom SSO handoff, not a general OAuth authorization server.
Existing SSO signing, 90-day JWT lifetime, issuer session storage, and other clients remain unchanged.
The client must opt in; client-side checks are convenience checks, while issuer checks enforce the binding.
Delivery order is issuer code support, then the Blog client, then this Blog-only
legacy bearer rejection. Do not merge or deploy the final cutover before the new client is
delivered. This change is a separate dependent cutover deliverable. This change does not require credential rotation.

The registered client is `blog`, with redirect URI exactly `https://blog.laisky.com`.
Its root callback carries `sso_code` and `sso_state`, never the reusable bearer.
The existing SSO login `redirect_to` embeds exactly four markers: `sso_flow=code`,
`sso_state` (32-byte base64url), `sso_challenge` (S256), and `sso_challenge_method=S256`.
An explicit `sso_flow` on any target must validate as the registered Blog code flow.
Partial markers on the fixed Blog origin never fall back to legacy bearer URLs.
Other clients without `sso_flow` preserve existing state/challenge parameters. Password, email-code, TOTP,
passkey, existing-session, and GitHub completion share the same async handoff.

POST `/sso/code` uses the existing active EdDSA SSO bearer in Authorization and JSON
`{client_id,redirect_uri,state,code_challenge,code_challenge_method}`.
POST `/sso/token` uses JSON `{client_id,redirect_uri,code,state,code_verifier}` and returns
`{access_token,token_type:"Bearer",expires_in}`. Existing public API prefix routing applies;
the canonical client endpoint is `https://sso.laisky.com/sso/token`.
Codes use 32 random bytes and digest-only Redis keys, expire within 60 seconds,
and consume exactly once across replicas after matching all bindings. Wrong bindings
do not burn a valid code. Redemption also revalidates the active SSO session.
Only the Blog origin may exchange through a browser; issuance permits exactly the documented
standalone SSO and MCP frontend origins (https://sso.laisky.com, https://mcp.laisky.com).
The MCP-mounted login uses the configured public API prefix, including /mcp/sso/code.
Successful authentication persists the existing origin-local session before code issuance;
a handoff failure keeps that session available for a validated retry and never navigates with a bearer.
Responses are no-store and no-referrer. Bodies and execution time are bounded.

Residual scope: JavaScript session storage and 90-day JWT semantics remain.
Unsafe legacy Blog targets are rejected at the raw redirect parser and asynchronous
handoff; the synchronous bearer builder rejects every Blog target. Blog identity is its normalized canonical
hostname, covering old paths, HTTP, ports, userinfo, and trailing-dot variants.
Code callbacks still require the exact HTTPS root. Other hostname clients preserve
legacy handoff. Legitimate unmarked Blog bookmarks instead navigate to a credential-free canonical
Blog bootstrap. The consumer starts a fresh code request with its own state and
S256 verifier; no session or reusable credential is appended to the old target.
Authentication-bearing and ambiguous legacy targets remain rejected.
This change addresses the remaining issuer migration path; it does not rotate existing
credentials, alter existing JWT lifetimes, or redesign issuer/browser session storage.
A code can still appear in request-target logs; PKCE protects redemption of an intercepted
code. This work does not inspect or change edge/origin log retention.

Primary references (reviewed 2026-10-09):
- RFC 7636 §§4.1–4.6: https://www.rfc-editor.org/rfc/rfc7636
- RFC 9700 §§2.1.1,4.1: https://www.rfc-editor.org/rfc/rfc9700.html
- Redis atomic script execution: https://redis.io/docs/latest/develop/programmability/eval-intro/

Refs Laisky/laisky-blog#188. Delivery remains pending the ordered issuer/client/cutover steps.

## Legacy bookmark bootstrap (2026-10-11)

The issuer may navigate an ordinary Blog bookmark to
https://blog.laisky.com/?sso_start=1&sso_return=<internal-route>.
This is an initiation URL, never an authorization response. Only the fixed root,
bootstrap marker and bounded same-origin return route travel to the consumer.
Root/article routes retain their safe query and fragment. Exact Blog host casing
and default ports normalize; old HTTP/default port80 bookmarks upgrade to fixed HTTPS.
Userinfo, non-default ports, trailing-dot hostnames, raw controls/backslashes,
protocol-relative inputs and authentication/partial-code markers are rejected.

The updated consumer removes all bootstrap/callback markers synchronously,
creates independent 32-byte state and verifier values in its own sessionStorage,
and sends only state plus S256 challenge back through the existing issuer61 flow.
An already verified issuer session completes without another credential prompt.
When the actual browser origin is exactly https://mcp.laisky.com, the issuer adds
only sso_issuer=mcp. The consumer maps this enum to the fixed MCP login entry
https://mcp.laisky.com/sso/login; absent/sso selects https://sso.laisky.com/.
Neither input can supply an arbitrary issuer, callback or redemption URL.
The issuer cannot supply or preserve a verifier on the Blog origin's behalf.
Callback registration, 60-second single-use codes, active-account checks, existing
JWT lifetime/storage and other hostname clients remain unchanged.

The bootstrap accepts exactly one sso_start=1 and one safe sso_return, with an optional single sso_issuer=sso|mcp,
no callback markers or other top-level query fields. Known protocol marker case
aliases are rejected in legacy targets/return routes and cleaned before optional
scripts in the consumer; arbitrary application query fields remain compatible. Failures do not exchange tokens
or alter existing authentication. Cleanup failure keeps App and optional scripts
unmounted. State/PKCE checks and one-time redemption remain mandatory after
bootstrap; malformed code requests never become bootstrap requests.

Deliver issuer61, then the consumer195 bootstrap, then this issuer cutover.
The canonical Blog root must serve the updated no-store consumer before cutover.
Obsolete bundles cannot receive a safe reusable-bearer handoff; retain a bootstrap/code-capable consumer during rollback; an obsolete consumer
that requires reusable URL bearers is incompatible with the final cutover.

Primary references reviewed 2026-10-11:
- RFC 7636 section4: https://www.rfc-editor.org/rfc/rfc7636.html#section-4
- RFC 9700 sections2.1/4.1/4.3: https://www.rfc-editor.org/rfc/rfc9700.html
- MDN sessionStorage: https://developer.mozilla.org/en-US/docs/Web/API/Window/sessionStorage
  (storage is origin/tab scoped, but opener-created tabs may initially receive a
  copy; initiation uses same-tab navigation and server replay prevention).

Known protocol marker case aliases in the current query or fragment are cleaned
and rejected before exchange, App or optional telemetry. A mixed valid code query
and reserved fragment cannot redeem. Ordinary nonprotocol article fragments
remain unchanged; no generic credential-name filtering is introduced.

Browser references reviewed 2026-10-11:

- https://developer.mozilla.org/en-US/docs/Web/API/Window/localStorage
  (issuer storage belongs to its origin; the fixed MCP entry preserves that origin).
- https://developer.mozilla.org/en-US/docs/Web/API/History/replaceState
  (same-origin URL cleanup updates the current history entry without loading a document).
