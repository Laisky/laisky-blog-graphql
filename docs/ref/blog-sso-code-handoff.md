# Opt-in Blog SSO code handoff

This is a scoped custom SSO handoff, not a general OAuth authorization server.
Existing SSO signing, 90-day JWT lifetime, issuer session storage, and other clients remain unchanged.
The client must opt in; client-side checks are convenience checks, while issuer checks enforce the binding.
Deploy the issuer before enabling the Blog client. This change does not require credential rotation.

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
Only the Blog origin may exchange through a browser; issuance is scoped to the SSO origin.
Responses are no-store and no-referrer. Bodies and execution time are bounded.

Residual scope: JavaScript session storage and 90-day JWT semantics remain.
Unmarked legacy Blog redirects remain supported for issuer-first migration, so an old
bookmarked legacy login URL can still put a reusable bearer in the initial Blog HTTP
request. Enabling the new client does not eliminate that migration path. A separately
ordered issuer cutover must reject unmarked Blog handoffs after the new client is
delivered; other clients remain compatible. This support change alone does not close #188.
A code can still appear in request-target logs; PKCE protects redemption of an intercepted
code. This work does not inspect or change edge/origin log retention.

Primary references (reviewed 2026-10-09):
- RFC 7636 §§4.1–4.6: https://www.rfc-editor.org/rfc/rfc7636
- RFC 9700 §§2.1.1,4.1: https://www.rfc-editor.org/rfc/rfc9700.html
- Redis atomic script execution: https://redis.io/docs/latest/develop/programmability/eval-intro/
