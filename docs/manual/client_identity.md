# Client identity behind reverse proxies

The HTTP service decides each request's client address with one rule, in
`library/clientip`. Everything that needs the client address uses that rule:

- the adaptive-authentication risk key that decides when SSO login and
  registration need a Turnstile challenge ([SSO manual](sso.md#cloudflare-turnstile));
- the `remoteip` sent to Cloudflare Turnstile siteverify;
- the `client_ip` field on the gin access log, on every request-scoped handler
  log, and on the MCP request logs. `remote` and `remote_addr` still record the
  connection peer.

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

Cloudflare appends the address it saw to any client-supplied `X-Forwarded-For`,
so the rightmost entry it forwards is the real visitor. The walk then gives the
real visitor in each of these cases:

- nginx overwrites the header (`X-Forwarded-For $remote_addr`);
- nginx appends to it (`$proxy_add_x_forwarded_for`);
- nginx passes Cloudflare's header through unchanged.

Reading `X-Real-IP` first would not be safe. A vhost that does not set
`X-Real-IP` forwards whatever value the client sent.

If nginx does not restore the visitor address with the realip module, the
rightmost entry is a Cloudflare edge address. Every visitor would then share a
few keys. Either configure the realip module, or add
[Cloudflare's published ranges](https://www.cloudflare.com/ips/) to
`trusted_proxies`.

## Ingress requirements (operator action)

On every nginx vhost that proxies to this service:

```nginx
proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for; # or $remote_addr to overwrite
proxy_set_header X-Real-IP $remote_addr;
```

Also restrict origin access to Cloudflare. Use a firewall allowlist of
Cloudflare's ranges, Authenticated Origin Pulls, or a Cloudflare Tunnel.

**Residual risk.** Suppose a vhost forwards the client's `X-Forwarded-For`
unchanged, and a caller reaches nginx directly, bypassing Cloudflare. That caller
can still choose its address, because the service sees the trusted nginx peer.
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
