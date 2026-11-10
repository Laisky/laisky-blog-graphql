# Client identity behind proxies: sources (2026-11-08)

Sources checked for issue #84, the trusted-proxy client identity fix and the
account-keyed failure budget. The code is in `library/clientip/` and
`internal/web/blog/controller/turnstile_risk.go`. The manual is
`docs/manual/client_identity.md`.

## Gin v1.12.0 `ClientIP` semantics (verified in module source)

Source: `gin@v1.12.0/gin.go` and `context.go` in `$(go env GOMODCACHE)`.

- `gin.New()` sets `ForwardedByClientIP: true`, `RemoteIPHeaders: {"X-Forwarded-For", "X-Real-IP"}` and
  `trustedProxies: {"0.0.0.0/0", "::/0"}`. By default every peer may assert any address.
- `ClientIP()` checks `TrustedPlatform` first and returns that header's raw value. It does not parse
  the value and does not check the peer. It also always trusts a unix-socket listener.
- Otherwise it parses `RemoteAddr`. It consults forwarding headers only when the peer is inside
  `trustedCIDRs`.
- For each header it joins every header line with commas (`Header.Values`). `validateHeader` walks
  the list from the right and returns the first address that is not a trusted proxy, or the leftmost
  address when all are trusted.
- An unparseable entry stops the walk and marks the header invalid. `ClientIP()` then **moves on to
  the next header** (X-Real-IP), and finally returns the peer.
- `SetTrustedProxies(nil)` leaves `trustedCIDRs` nil, so `ClientIP()` returns the peer.
- Gin docs: "Gin trusts all proxies by default if you don't specify a trusted proxy … this is NOT safe".
  <https://gin-gonic.com/en/docs/server-config/trusted-proxies/> (updated 2026-10-31).

## Choosing the client from X-Forwarded-For

- MDN: security uses "*must only* use IP addresses added by a trusted proxy". "The full list of IPs
  (from all `X-Forwarded-For` headers) must be used". With a trusted-proxy list, search "from the
  rightmost, skipping all addresses that are on the trusted proxy list."
  <https://developer.mozilla.org/en-US/docs/Web/HTTP/Reference/Headers/X-Forwarded-For> (modified 2025-07-04).
- adam-p, "The perils of the 'real' client IP":
  <https://adam-p.ca/blog/2022/03/x-forwarded-for/> (updated 2026-06-06).
  - Prefer a trusted-IP list over a hop count.
  - "Using a value that's not actually an IP address is never the right choice."
  - "There's *never* a time when you're okay with just falling back across a big list of header values."
- realclientip-go (the reference library from the same author) warns against chaining strategies to
  check several headers, because that enables spoofing. <https://github.com/realclientip/realclientip-go>
- RFC 7239 §8.1: forwarding headers "cannot be relied upon to be correct".
  <https://www.rfc-editor.org/rfc/rfc7239.html>

Design consequence: we read only X-Forwarded-For by default. This avoids gin's fallback to
X-Real-IP after a malformed X-Forwarded-For entry. A malformed chain resolves to the proxy peer,
which coarsens identity and never lets the client choose it.

## Cloudflare and nginx behaviour

- Cloudflare: if a request has no `X-Forwarded-For`, Cloudflare sets one with the same value as
  `CF-Connecting-IP`. If one exists, "Cloudflare will append the IP address of the HTTP proxy
  connecting to Cloudflare to the header". So the rightmost entry is the address Cloudflare saw.
  `True-Client-IP` is Enterprise-only and identical to `CF-Connecting-IP`.
  <https://developers.cloudflare.com/fundamentals/reference/http-headers/> (updated 2026-10-28).
  - The page does not cover multiple XFF header lines or a client-supplied `X-Real-IP` for ordinary
    (non-Worker) traffic. Treat both as unverified.
  - Pseudo-IPv4 mode and the "Remove visitor IP headers" Managed Transform rewrite or remove these
    headers.
- nginx realip: `set_real_ip_from` "Defines trusted addresses that are known to send correct
  replacement addresses". With `real_ip_recursive off` (the default) the client address becomes "the
  last address sent in the request header field".
  <https://nginx.org/en/docs/http/ngx_http_realip_module.html>
- nginx `$proxy_add_x_forwarded_for` is "the 'X-Forwarded-For' client request header field with the
  `$remote_addr` variable appended to it". The left part stays client-controlled. The rightmost entry
  is what nginx itself saw. <https://nginx.org/en/docs/http/ngx_http_proxy_module.html>

## IPv6 keying

- RFC 7934 (BCP 204) §6: networks may give "the host with a dedicated /64 prefix", and doing so is
  "operationally prudent". One host can therefore rotate through a whole /64.
  <https://www.rfc-editor.org/rfc/rfc7934.html>
- Better Auth GHSA-p6v2-xcpg-h6xw / CVE-2026-45364 (2026-05): keying rate limits on the exact IPv6
  text let sign-in limits be bypassed. The fix treats each IPv6 /64 as one client.
  <https://github.com/better-auth/better-auth/security/advisories/GHSA-p6v2-xcpg-h6xw>
- Wider buckets (/56 or /48) would be safer. RFC 6177 and RIPE-690 recommend those sizes for end
  sites. They would also merge more unrelated users, so /64 is the conservative default here.

## Account-keyed failure counters

- OWASP Authentication Cheat Sheet: "The counter of failed logins should be associated with the
  account itself, rather than the source IP address". Lockout must not let attackers cause "a denial
  of service by locking out other users' accounts". A CAPTCHA after a few failures is the friendlier
  option. <https://cheatsheetseries.owasp.org/cheatsheets/Authentication_Cheat_Sheet.html>
- OWASP Credential Stuffing Cheat Sheet: IP blocking "should not be used as the sole or primary
  defense". <https://cheatsheetseries.owasp.org/cheatsheets/Credential_Stuffing_Prevention_Cheat_Sheet.html>
- NIST SP 800-63B-4 §3.2.2 (final 2025-07-31) lists "a bot detection and mitigation challenge" as an
  accepted rate-limiting measure. After a successful authentication the verifier "SHOULD disregard any
  previous failed attempts" and "SHOULD reset the retry count". <https://pages.nist.gov/800-63-4/sp800-63b.html>

Design consequence: an account under attack only ever escalates to a Turnstile challenge. It is
never locked out. A full success resets its counter.

## Review follow-up sources (2026-11-10)

### Cloudflare passes request headers through

<https://developers.cloudflare.com/fundamentals/reference/http-headers/>, updated 2026-10-28:

- "Cloudflare passes all HTTP request headers to your origin web server and adds additional headers".
  The only exception it states is for header names NGINX considers invalid.
- A client-supplied `X-Forwarded-For` is kept and the proxy address is appended. The page does not
  say what happens to **several** `X-Forwarded-For` lines; still unverified.
  - A Cloudflare incident titled "Duplicated values in X-Forwarded-For header" was found only on a
    third-party status mirror, without details.
  - The manual therefore does not rely on a passed-through chain: every vhost must set the header.
- `X-Forwarded-Proto`: "If the client set a different value, it will be overwritten."
- `X-Real-IP` "is stripped" when no Worker subrequest is triggered. The statement sits in the Worker
  subrequest section, so verify it before relying on it.
- No page explicitly covers `X-Forwarded-Host`. The general pass-through rule applies, and the
  default cache key includes `x-forwarded-host`
  (<https://developers.cloudflare.com/cache/how-to/cache-keys/>, updated 2026-10-30). Treat it as
  client-controlled.

### nginx forwards client headers unless overridden

<https://nginx.org/en/docs/http/ngx_http_proxy_module.html>:

- Only `Host` and `Connection` are redefined by default. `proxy_pass_request_headers on` is the
  default, so a client's `X-Forwarded-Host` reaches the upstream unless `proxy_set_header` replaces it.
- `proxy_set_header` directives "are inherited from the previous configuration level if and only if
  there are no `proxy_set_header` directives defined on the current level."

Design consequence: `X-Forwarded-Host` and `X-Forwarded-Proto` count only from trusted proxies, and
the operator must set them on every vhost. Because they are still unauthenticated, the request host
may choose among configured Turnstile pairs but never disable the gate.

### Turnstile hostname management

- <https://developers.cloudflare.com/turnstile/additional-configuration/hostname-management/>, updated
  2026-04-27: "adding a hostname automatically authorizes all of its subdomains". There are no
  wildcards, and Free plans allow up to 10 hostnames per widget.
- An unlisted host fails with client error `110200` "Domain not authorized"
  (<https://developers.cloudflare.com/turnstile/troubleshooting/client-side-errors/error-codes/>, updated
  2026-09-25).

Design consequence: runtime-config serves the default pair's widget key to hosts without their own
pair, and the operator lists the apex domain on that widget.

### Collation folding for account keys

`golang.org/x/text/collate` with `language.Und` and `collate.Loose` compares at primary strength
(IgnoreCase, IgnoreDiacritics, IgnoreWidth). It is CLDR-root collation, which is based on the UCA, as
MySQL's `utf8mb4_0900_ai_ci` is.

Checked locally: `alice`, `ALICE`, `alicé`, `àlîcë`, `ａｌｉｃｅ`, `al<U+200B>ice` and
`alice<U+0301>` produce one key. So do `straße`/`strasse` and `ﬁle`/`file`. Punctuation stays
significant (`a-lice` differs), as in MySQL's NO PAD `_0900_` collations. This approximates the MySQL
collation without claiming to be identical to it; over-folding only produces challenges sooner.
