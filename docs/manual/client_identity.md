# Client identity behind reverse proxies

The HTTP service decides each request's client address with one rule, in
`library/clientip`. Everything that needs the client address uses that rule:

- the adaptive-authentication risk key that decides when SSO login and
  registration need a Turnstile challenge ([SSO manual](sso.md#cloudflare-turnstile));
- the `remoteip` sent to Cloudflare Turnstile siteverify;
- the `client_ip` field on the gin access log, on every request-scoped handler
  log, and on the MCP request logs. `remote` and `remote_addr` still record the
  connection peer.

The same rule decides when `X-Forwarded-Host` and `X-Forwarded-Proto` count
(see [Forwarded host and scheme](#forwarded-host-and-scheme)). They select the
site branding, the Turnstile pair and the redirect and WebAuthn origin.

## Rule

The client address is the connection peer (`RemoteAddr`). The one exception is
a peer inside `settings.web.trusted_proxies`. Only then is the forwarding header
read:

1. All `X-Forwarded-For` lines are joined into one chain.
2. The chain is walked from the right, skipping addresses that are trusted proxies.
3. The first address that is not a trusted proxy is the client. If every entry
   is trusted, the leftmost entry is the client.
4. If the walk meets an entry that is not a bare IP address (a hostname, an
   `ip:port`, an empty item), the client is the proxy peer itself. No other
   header is tried.

A client therefore cannot choose its address by sending `X-Forwarded-For` or
`X-Real-IP` directly. Entries a client prepends to the chain are never reached,
because the walk stops at the address the first trusted proxy recorded. Platform
headers such as `CF-Connecting-IP` are never trusted directly, because the
service cannot tell whether the request really passed through Cloudflare.

## Forwarded host and scheme

`X-Forwarded-Host` and `X-Forwarded-Proto` count only when the connection peer
is inside `settings.web.trusted_proxies`. From any other peer they are ignored,
and the `Host` header and the connection's own scheme are used.

- All header lines are joined into one list. The rightmost entry counts: it is
  the one the proxy nearest to this service added.
- A host must be a plain `host` or `host:port`, an IPv6 literal in brackets
  allowed. A scheme must be `http` or `https`. Any other value is ignored.
- nginx forwards a client-supplied `X-Forwarded-Host` unchanged unless the vhost
  sets it ([proxy module](https://nginx.org/en/docs/http/ngx_http_proxy_module.html):
  original request headers are passed unless `proxy_set_header` overrides them).
  Cloudflare passes request headers through as well. A trusted peer is therefore
  not enough on its own: every vhost must set these headers itself (see the
  [ingress requirements](#ingress-requirements-operator-action)).

The request host is never authenticated, so it may only choose *which*
configured variant of a control applies, never *whether* one applies. For
example, Turnstile always has a secret once any secret is configured (see the
[SSO manual](sso.md#cloudflare-turnstile)).

## Settings

```yaml
settings:
  web:
    # IPs or CIDRs of the reverse proxies allowed to report a client address.
    # Unset uses the default below; [] trusts no proxy (direct deployment).
    trusted_proxies:
      - 127.0.0.0/8
      - 10.0.0.0/8
      - 172.16.0.0/12
      - 192.168.0.0/16
      - 100.64.0.0/10 # carrier-grade NAT, used by Tailscale
      - ::1/128
      - fc00::/7
    # Forwarding headers read from a trusted proxy. Unset reads only X-Forwarded-For.
    client_ip_headers:
      - X-Forwarded-For
```

| Setting | Default when unset | Notes |
| --- | --- | --- |
| `settings.web.trusted_proxies` | loopback, RFC 1918, `100.64.0.0/10`, `::1/128`, `fc00::/7` | Entries are IPv4/IPv6 addresses or CIDRs. `[]` trusts no proxy. A comma-separated string is also accepted. |
| `settings.web.client_ip_headers` | `[X-Forwarded-For]` | Read in order. A later header is tried when an earlier one is absent or malformed, so list a second header only if every trusted proxy overwrites it. `[]` ignores every forwarding header. |

Invalid values fail startup with an error that names the entry. This covers a
hostname, a malformed CIDR, a CIDR with host bits set (`10.0.0.1/8`), an IPv6
zone, or a malformed header name. The service never falls back to trusting
every peer.

An IPv4-mapped entry (`::ffff:a.b.c.d/n` with `n` of 96 or more, or a mapped
bare address) means the IPv4 network it covers, because that is how gin
matches peers. For example `::ffff:10.0.0.0/104` is `10.0.0.0/8`, and
`::ffff:0:0/96` is `0.0.0.0/0`, which trusts every IPv4 peer and triggers the
startup warning. A mapped prefix shorter than /96 is rejected.

At startup the service logs the effective policy once:
`configured client identity policy` with `trusted_proxies`,
`trusted_proxies_source` (`default` or `config`), `client_ip_headers` and
`trusts_every_peer`. If a configured network covers the whole address space
(`0.0.0.0/0` or `::/0`), the line is a warning, because any caller can then
choose its address.

For a deployment without a reverse proxy, set `trusted_proxies: []`.

## Reference topology

The production layout is Cloudflare → nginx → Go service:

1. nginx restores the visitor address from Cloudflare
   (`set_real_ip_from <Cloudflare ranges>; real_ip_header CF-Connecting-IP;`).
2. nginx proxies to the Go service over a private or Tailscale address. The Go
   process sees a peer inside the default trusted networks, such as the
   Tailscale address or a docker bridge gateway.

When nginx sets `X-Forwarded-For` itself, the walk gives the real visitor:

- `X-Forwarded-For $remote_addr` overwrites the chain with the address nginx
  restored;
- `$proxy_add_x_forwarded_for` appends that address, so it is the rightmost
  entry, and anything the client prepended is never reached.

A vhost that passes Cloudflare's `X-Forwarded-For` through unchanged is **not**
a supported layout. Cloudflare documents that it appends the visitor address to
an existing header. It does not document what it does when a request carries
several `X-Forwarded-For` lines. The rightmost entry of a passed-through chain
is therefore not guaranteed to be the visitor.

Reading `X-Real-IP` first would not be safe. A vhost that does not set
`X-Real-IP` forwards whatever value the client sent.

If nginx does not restore the visitor address with the realip module, the
rightmost entry is a Cloudflare edge address. Every visitor would then share a
few keys. Either configure the realip module, or add
[Cloudflare's published ranges](https://www.cloudflare.com/ips/) to
`trusted_proxies`.

## Ingress requirements (operator action)

Every nginx vhost that proxies to this service must set:

```nginx
proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for; # or $remote_addr to overwrite
proxy_set_header X-Real-IP $remote_addr;
proxy_set_header X-Forwarded-Host $http_host;
proxy_set_header X-Forwarded-Proto $scheme; # or https behind a TLS-terminating CDN
```

`$http_host` keeps a non-default port, which the service needs to build redirect
bases, the GitHub callback and the WebAuthn origin; `$host` drops it. Site and
Turnstile matching ignore the port either way.

nginx inherits `proxy_set_header` from an enclosing level only when the current
level defines none, so repeat them in every `location` that sets its own headers.

Also restrict origin access to Cloudflare. Use a firewall allowlist of
Cloudflare's ranges, Authenticated Origin Pulls, or a Cloudflare Tunnel.

**Residual risk.** Suppose a vhost forwards the client's `X-Forwarded-For` or
`X-Forwarded-Host` unchanged. A caller can then choose its address (when it
reaches nginx directly, bypassing Cloudflare) or the host the service believes
(always), because the service sees the trusted nginx peer. A chosen host can
only select among configured pairs and origins, never switch the Turnstile gate
off, but it still changes branding and redirect bases.
Any host inside the trusted networks (tailnet, docker networks, LAN) can do the
same. Close this by fixing the vhost headers above and restricting origin
access. To narrow trust further, list only the exact proxy addresses (for
example the nginx host's Tailscale address and the docker bridge gateway) in
`trusted_proxies`.

The account-keyed failure budget (see the [SSO manual](sso.md#cloudflare-turnstile))
limits what such spoofing can achieve. Guesses against one account escalate to a
challenge after `account_failure_threshold` failures, whatever address each
guess claims.

## Compatibility and migration

- Before this change, gin trusted every peer and read `X-Forwarded-For`, then
  `X-Real-IP`. Deployments whose proxy connects from a private, loopback, CGNAT
  or unique-local address keep working with the default.
- If your proxy connects from any other address, list it in `trusted_proxies`.
  Otherwise all clients share the proxy's key and are challenged together.
- `X-Real-IP` is no longer read by default. If your ingress sets only
  `X-Real-IP`, either make it set `X-Forwarded-For` (preferred) or configure
  `client_ip_headers: [X-Real-IP]`.
- The adaptive-authentication tracker keys IPv6 clients by their /64 network
  ([RFC 7934](https://www.rfc-editor.org/rfc/rfc7934.html) lets one host own a
  whole /64). Turnstile `remoteip` and logs keep the full address.
- No stored data changes. The risk tracker is in memory only.

Research notes and sources: [docs/ref/client_ip_identity_20261108.md](../ref/client_ip_identity_20261108.md).
