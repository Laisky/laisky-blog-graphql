# SSO redirect policy and registered-client code handoff (2026-11-08)

Primary sources reviewed for issue 89 (long-lived SSO bearer in redirect URLs).
Quotes are from the plain-text documents; section numbers refer to those versions.

## Sources

- RFC 9700, OAuth 2.0 Security Best Current Practice (January 2025): <https://www.rfc-editor.org/rfc/rfc9700>
- draft-ietf-oauth-v2-1-17, OAuth 2.1 (31 October 2026, still an Internet-Draft): <https://www.ietf.org/archive/id/draft-ietf-oauth-v2-1-17.txt>
- RFC 8252, OAuth 2.0 for Native Apps: <https://www.rfc-editor.org/rfc/rfc8252>
- RFC 7636, PKCE: <https://www.rfc-editor.org/rfc/rfc7636>
- RFC 6749 and RFC 6750: <https://www.rfc-editor.org/rfc/rfc6749>, <https://www.rfc-editor.org/rfc/rfc6750>
- WHATWG URL Standard (backslash and credential handling): <https://url.spec.whatwg.org/>

## Bearer tokens in URLs

- RFC 9700 §4.3.2: "Clients MUST NOT pass access tokens in a URI query parameter."
- RFC 9700 §2.1.2: response types that issue access tokens in the authorization response
  are vulnerable to leakage and replay; the code flow keeps tokens out of URLs.
- RFC 6750 §2.3 and §5.3: the query method "SHOULD NOT be used" because URLs are logged;
  bearer tokens "SHOULD NOT be passed in page URLs" and should be short-lived.
- OAuth 2.1 -17 §7.1.3.7 tightens this to "Bearer tokens MUST NOT be passed in page URLs".

Applied: registered clients only ever receive a single-use code. The legacy `sso_token`
query handoff remains for unregistered clients until `legacy_token_redirect: deny`.

## Redirect URI matching and transport

- RFC 9700 §2.1 and §4.1.3: exact string matching; wildcard patterns are bypassable (§4.1.1).
- OAuth 2.1 -17 §2.3.1 and §4.1.1: register the complete redirect URI; case-sensitive,
  character-by-character comparison without normalization.
- OAuth 2.1 -17 §1.5: all OAuth URLs "MUST use the https scheme except for loopback
  interface redirect URIs, which MAY use the http scheme".
- RFC 6749 §10.5: a client relying on the code for user authentication "MUST require the use of TLS".
- RFC 8252 §8.3 and OAuth 2.1 §8.4.2: loopback IP literals are preferred; "localhost" is
  NOT RECOMMENDED for native apps because of name resolution and listening interfaces.
- No source addresses private-network IP literals over http. OAuth 2.1 -17 §1.5 admits only
  loopback, whose traffic never leaves the device. Private literals (RFC 1918, RFC 6598 /
  Tailscale, IPv6 ULA) are answered by any device on that network, so a production issuer
  hands them the legacy bearer only at an exact registered callback; an issuer that itself
  runs on a development host keeps them (review follow-up, 2026-11-10). `localhost` stays a
  loopback host; browsers resolve it to loopback.

Applied: registered callbacks are compared byte for byte; production hosts require https.

## Authorization code

- RFC 6749 §4.1.2 and §10.5: short-lived (at most ten minutes recommended), single-use,
  bound to the client identifier and redirect URI.
- OAuth 2.1 -17 §4.1.1 and §7.5.2: PKCE is mandatory, S256 only; `plain` is forbidden.
- OAuth 2.1 -17 §4.1.3: return an access token only once per code.

Applied: 60-second codes, S256 only, one-time Redis redemption bound to client ID,
exact redirect URI, state and verifier; redemption from a browser is bound to the
callback's origin.

## Backslashes

- WHATWG URL parsing treats `\` as `/` in the scheme, authority and path of special schemes
  (validation error `invalid-reverse-solidus`); Go `net/url` does not. Checked locally
  (Node 22, Go 1.27): `https://evil.example\@sso.laisky.com/` is host `evil.example` in
  WHATWG and an invalid userinfo in Go; `https://chat.laisky.com/a\b` has path `/a/b` in
  WHATWG and `/a\b` in Go.
- In the query and fragment, WHATWG neither rewrites nor percent-encodes `\` (it is not in
  the query or fragment percent-encode sets): `https://chat.laisky.com/p?q=a\b#f\g` keeps
  both backslashes in `href`, and both parsers agree on the host. The policy accepts `\`
  only after the first `?` or `#`.

## Open redirectors and unregistered URIs

- RFC 9700 §2.1 and §4.11: authorization servers must not expose open redirectors and
  should redirect automatically only to trusted URIs.
- RFC 6749 §3.1.2.4 and §4.1.2.1: on an invalid or mismatching URI, inform the user and
  "MUST NOT automatically redirect the user-agent to the invalid redirection URI".
- RFC 6749 §3.1.2: a redirect URI "MUST NOT include a fragment component".
- No OAuth document discusses userinfo or backslashes. WHATWG URL parsing treats `\` as
  `/` for special schemes and accepts credentials, so parse-then-compare invites parser
  differentials; this repository rejects both from the raw string, before parsing.

## Not adopted

- The `iss` authorization-response parameter (OAuth 2.1 -17 §4.1.2, RFC 9207) defends
  clients that talk to several authorization servers. Every client here uses one issuer
  and redeems at the fixed `https://sso.laisky.com/sso/token`, so the callback carries
  only `sso_code` and `sso_state`, matching the Blog contract.
