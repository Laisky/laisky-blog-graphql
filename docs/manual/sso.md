# SSO Manual

This manual covers Laisky SSO integration for client applications and operational configuration for the SSO service.

## Menu

- [SSO Manual](#sso-manual)
  - [Audience](#audience)
  - [Public Identity Contract](#public-identity-contract)
  - [User Capabilities](#user-capabilities)
  - [Client Integration Flow](#client-integration-flow)
  - [Login Entry URL](#login-entry-url)
  - [`redirect_to` Validation Rules](#redirect_to-validation-rules)
  - [Registered Client Code Flow](#registered-client-code-flow)
  - [Callback Contract](#callback-contract)
  - [Legacy Bearer Cutover](#legacy-bearer-cutover)
  - [JWT Token Specification](#jwt-token-specification)
  - [Token Validation](#token-validation)
  - [Account and Profile Flows](#account-and-profile-flows)
  - [Data and Migration Notes](#data-and-migration-notes)
  - [Admin Configuration](#admin-configuration)
  - [GraphQL API Reference](#graphql-api-reference)
  - [Security Notes](#security-notes)
  - [Common Failure Cases](#common-failure-cases)
  - [Go-Live Checklist](#go-live-checklist)

## Audience

- Product teams integrating login with Laisky SSO.
- Backend engineers implementing callback and session exchange.
- Frontend engineers implementing SSO redirects.
- Operators configuring SSO login methods, SMTP email delivery, GitHub OAuth, WebAuthn passkeys, and Cloudflare Turnstile.

## Public Identity Contract

Every SSO user has a stable external `uid` generated as UUIDv7. This UID is the only user identifier intended for external systems.

Public surfaces that expose the external UID:

- `UserProfile.uid`.
- `BlogUser.id` in SSO GraphQL responses, including `WhoAmI`, login responses, GitHub OAuth responses, and passkey login responses.
- JWT `sub`.
- JWT `uid`.

Do not depend on internal storage identifiers. SSO-facing APIs must use the external UID for user identity.

## User Capabilities

The SSO service supports these account and authentication flows:

- Email account registration with password and an SMTP-delivered verification code.
- Email verification-code login.
- GitHub-backed registration and login through GitHub OAuth.
- Password login with optional TOTP verification.
- Discoverable passkey login.
- Profile page for:
  - Viewing the stable external user UID.
  - Reviewing account and enabled authentication methods.
  - Changing password.
  - Binding GitHub OAuth identity.
  - Binding and disabling TOTP.
  - Registering passkeys.

GitHub user authentication is implemented with GitHub OAuth authorization-code flow. GitHub Actions OIDC is not used for end-user login.

## Client Integration Flow

New and migrating clients use the [registered client code flow](#registered-client-code-flow):

1. The operator registers your client ID and its exact callback URI.
2. Your application redirects the browser to the SSO login page with a code request in `redirect_to`.
3. The user signs in with one of the enabled methods:
   - Password, plus TOTP when the user has TOTP enabled.
   - Email verification code.
   - Passkey.
   - GitHub.
4. SSO redirects the browser to your exact callback with a single-use `sso_code` and your `sso_state`.
5. Your application redeems the code at `https://sso.laisky.com/sso/token` with its PKCE verifier.
6. Your backend creates its own local session and redirects the browser to a clean URL.

Legacy flow (deprecated, unregistered clients only): SSO redirects to `redirect_to` with
`sso_token=<JWT>` in the query; your backend validates it, creates its own session and
removes the token from the URL. The operator can switch it off; see
[Legacy Bearer Cutover](#legacy-bearer-cutover).

Blog exception: `blog.laisky.com` never receives `sso_token`. Blog uses the opt-in
single-use code flow (`sso_flow=code` with S256 PKCE, then `sso_code` plus
`sso_state`, redeemed at `/sso/token`). Any other Blog target is either sent to a
credential-free bootstrap (`?sso_start=1&sso_return=<route>`) or rejected. See
[Blog SSO code handoff](../ref/blog-sso-code-handoff.md).

## Login Entry URL

Standalone SSO site:

```text
https://sso.laisky.com/?redirect_to={URL_ENCODED_CALLBACK_URL}
```

MCP-mounted SSO route:

```text
https://mcp.laisky.com/sso/login?redirect_to={URL_ENCODED_CALLBACK_URL}
```

Example:

```text
https://sso.laisky.com/?redirect_to=https%3A%2F%2Fapp.laisky.com%2Fauth%2Fsso%2Fcallback%3Fstate%3Dabc123
```

`redirect_to` should point to your backend callback endpoint and should include your own anti-CSRF `state`.

## `redirect_to` Validation Rules

The SSO frontend and backend enforce one policy, implemented in
`internal/library/ssoredirect` and `web/src/lib/sso-redirect-policy.ts` and pinned by the
shared vectors in `web/src/lib/sso-redirect-policy.v1.json`. The checks run on the raw
`redirect_to` string before URL parsing, so parser repairs cannot change the outcome.

- Form: an absolute `https://` URL, an absolute `http://` URL on a development host, or a
  path starting with a single `/` (an issuer page).
- At most 2048 characters of printable ASCII. Spaces, control characters and non-ASCII
  characters are rejected; browsers percent-encode them in `location.href`.
- Backslashes are rejected in the scheme, authority and path, where browsers read `\` as `/`
  but other parsers do not (`https://evil.example\@sso.laisky.com/`). Browsers leave `\`
  unencoded in the query and fragment of `location.href`, so it is accepted there.
- No userinfo (`user@` or `user:pass@`) in the authority.
- Production hosts, `https` only:
  - `laisky.com`.
  - Any `*.laisky.com` subdomain.
- Loopback hosts, `http` or `https`, from any issuer (the request never leaves the device):
  - `localhost`, canonical IPv4 in `127.0.0.0/8`, and IPv6 `[::1]`.
- Private development hosts, `http` or `https`:
  - Canonical dotted-decimal IPv4 in `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16` or
    `100.64.0.0/10` (CGNAT, for example Tailscale), and IPv6 `fc00::/7` in hexadecimal form.
  - These addresses are shared by a LAN or tailnet, so another device can answer them. A
    production issuer (`https://sso.laisky.com`) therefore never hands them the legacy bearer
    unless the exact callback is registered; see
    [Development callbacks](#development-callbacks). An issuer that itself runs on a
    loopback or private host keeps the legacy handoff for them.
- Numeric IPv4 spellings that parsers rewrite (`0x7f.1`, `127.1`, `010.0.0.1`, `2130706433`),
  IPv4-mapped or zone-scoped IPv6, ports outside 1–65535, and leading-zero ports are rejected.

The accepted target then takes exactly one path:

| Target | Result |
| --- | --- |
| `blog.laisky.com` | Blog code flow or bootstrap ([Blog SSO code handoff](../ref/blog-sso-code-handoff.md)); never the bearer |
| A registered client's exact code request | Single-use code to the registered callback |
| Any other URL on a registered client's hostname | Rejected (`code_flow_required`), unless the client sets `legacy_token_redirect: allow` |
| A page on the issuer origin that completes sign-in | Opened without a URL bearer; the session is already stored there |
| An unregistered private development target, from a production issuer | Rejected (`development_target_requires_registration`) |
| Any other accepted target, including the other issuer origin | Legacy `sso_token` handoff, only while `legacy_token_redirect` is `allow` |

The standalone and MCP-mounted issuers (`https://sso.laisky.com` and
`https://mcp.laisky.com/sso/login`) keep separate browser sessions. A page on the other
issuer origin is therefore a legacy target: under `allow` it receives the bearer in its
URL over https (both origins are this service); under `deny` it is refused. GitHub sign-in
completes on the configured callback origin (`settings.web.github_oauth.redirect_url`), so
the GitHub start mutation judges the target against that origin and refuses such a target
before the user leaves for GitHub.

`sso_flow` without `sso_client_id` is the Blog-only format and is rejected on other hosts.
Without `redirect_to`, sign-in opens `/profile` on the issuer origin that completes it (for
GitHub, the configured callback origin), so no bearer crosses to another origin.

Plain `http` on `*.laisky.com` was accepted before 2026-11-08 and is now rejected. All
known consumers send `https` (Accounting from its configured callback; go-ramjet CV and
GPTChat from the page URL), so a page opened over plain `http` must upgrade to `https`
before it starts sign-in.

## Registered Client Code Flow

A registered client receives only a single-use code bound to its client ID, its exact
callback URI, its `sso_state` and its S256 PKCE challenge. The reusable bearer never
appears in its URLs. This generalizes the Blog handoff; Blog keeps its own fixed format.

### Registration

The operator adds the client to `settings.web.sso.clients` (see
[Admin Configuration](#sso-redirect-policy-and-client-registry)). Registration rules:

- Client ID: `^[a-z][a-z0-9_-]{1,63}$`, not `blog`.
- One to eight callback URIs, each canonical and compared byte for byte: lower-case
  `https://` (or `http://` on a development host), lower-case host without a trailing dot
  or default port, an explicit path (`/` for the root) without `.`/`..` segments or
  percent escapes, and no query or fragment. A URI belongs to only one client.
- Registering a callback reserves its whole hostname: other URLs on that hostname are
  refused instead of receiving the bearer. A hostname, whatever the port, belongs to exactly
  one client; startup rejects a registry that gives one hostname to two clients.

### Code request

Generate a fresh `state` and PKCE verifier per sign-in, each from 32 random bytes encoded
as unpadded base64url (43 characters). Keep both in the user's browser session. Compute
`challenge = BASE64URL(SHA-256(verifier))` and send the browser to:

```text
https://sso.laisky.com/?redirect_to=URL_ENCODE(
  https://accounting.laisky.com/api/auth/sso/callback?sso_flow=code&sso_client_id=accounting
  &sso_state={state}&sso_challenge={challenge}&sso_challenge_method=S256)
```

The `redirect_to` value is the registered callback followed by exactly these five query
parameters in any order, each once, with no fragment. `https://mcp.laisky.com/sso/login`
accepts the same `redirect_to`.

### Callback

```text
{registered_callback}?sso_code={code}&sso_state={state}
```

1. Remove `sso_code` and `sso_state` from the URL before loading any third-party script.
2. Compare `sso_state` with the stored value; discard the stored state and verifier after use.
3. Redeem within 60 seconds:

```bash
curl -X POST 'https://sso.laisky.com/sso/token' \
  -H 'Content-Type: application/json' \
  -d '{"client_id":"accounting","redirect_uri":"https://accounting.laisky.com/api/auth/sso/callback",
       "code":"<sso_code>","state":"<state>","code_verifier":"<verifier>"}'
```

The response is `{"access_token":"<JWT>","token_type":"Bearer","expires_in":<seconds>}`
with `Cache-Control: no-store`. The access token is the same SSO JWT described below.

Redemption rules:

- A code is redeemed once. Wrong bindings fail with `invalid_grant` and do not consume it.
- Server-side redemption sends no `Origin`. Browser redemption is allowed only from the
  origin of the client's registered callback; any other origin gets `403`.
- Redemption rechecks that the session is still active.

### Development callbacks

A developer who signs in through the production issuer from a LAN or tailnet address (for
example `http://192.168.1.20:5173`) registers the exact callback:

```yaml
settings:
  web:
    sso:
      clients:
        devapp:
          redirect_uris:
            - http://192.168.1.20:5173/sso/callback
          # Only while the development consumer still reads sso_token.
          legacy_token_redirect: allow
```

- The code flow then works for that exact callback, the same as for production clients.
- With `legacy_token_redirect: allow` on the client and on the issuer, that exact callback
  (with any query) also receives the legacy bearer; other URLs on the host are refused.
- A registration reserves the whole hostname for one client, so two development apps on
  one address need separate hosts or one shared client entry.
- Loopback (`localhost`, `127.0.0.1`, `[::1]`) needs no registration, and an issuer that
  runs on a development host itself keeps the legacy handoff for private targets.

### Migration from the legacy handoff

Follow the same issuer, consumer, cutover order Blog used:

1. Issuer: register the client with `legacy_token_redirect: allow` on the client entry.
   Code requests now work, and the client's old `sso_token` handoff keeps working.
2. Consumer: ship the code request, callback and redemption above, and stop reading `sso_token`.
3. Cutover: remove the client's `legacy_token_redirect` (default `deny`). Its hostname now
   accepts only code requests.

## Callback Contract

Legacy handoff (deprecated, unregistered clients while `legacy_token_redirect` is `allow`).
Blog and registered clients never receive it.

```text
sso_token=<JWT>
```

Behavior:

- Existing query parameters are preserved.
- Existing `sso_token` is overwritten.
- GitHub and passkey login use a signed server state/session before redirecting, so the callback target is not trusted from the browser at finish time.
- GitHub and passkey completion re-apply the current policy to the signed target, so a
  sign-in started before a policy change cannot finish toward a now-refused target.

Your callback endpoint should:

1. Validate your own `state`.
2. Read `sso_token`.
3. Validate `sso_token`.
4. Create your own session, preferably with an HttpOnly secure cookie.
5. Redirect to a clean URL without `sso_token`.

## Legacy Bearer Cutover

`settings.web.sso.legacy_token_redirect` controls the legacy handoff for unregistered targets:

- `allow` (default): unregistered targets that pass the redirect policy receive `sso_token`.
- `deny`: unregistered targets are refused on every path (password, TOTP, email code,
  existing session, passkey and GitHub, in the frontend and in the GraphQL start and
  completion mutations). Blog, registered clients and pages on the issuer origin that
  completes sign-in keep working; a page on the other issuer origin is refused (at GitHub
  start for GitHub sign-in).

The issuer frontend reads the policy from `runtime-config.json` (`ssoRedirect`). If the
runtime configuration cannot be loaded, or the policy in it is malformed, the frontend
fails closed as if `deny` were set and asks the user to reload.

Remaining risk while `allow` is set: an unregistered client receives the 90-day SSO JWT in
its URL query, where browser history, the client's access logs, proxies and analytics can
retain it. Anyone who obtains that URL can act as the user until the token expires. Requiring
https on production hosts keeps the URL from passive observers between the browser and the
client's TLS endpoint; it does nothing about history, logs, proxies that terminate TLS,
analytics or an operator of the receiving host. Loopback targets may use plain http because
the request stays on the device, and private development targets need a development issuer
or a registered callback. The JWT lifetime is unchanged because clients use it as their session.

Owner cutover step: when every consumer that still reads `sso_token` (go-ramjet CV and
GPTChat, Accounting) has moved to the code flow, set `legacy_token_redirect: deny` and
restart the issuer. Roll back by setting `allow` again.

## JWT Token Specification

`sso_token` is a standard signed JWT. The payload is readable by clients; do not put secrets in claims.

- Signing algorithm: `EdDSA` with an Ed25519 key pair.
- Issuer: `laisky-sso`.
- Current TTL: about 90 days (`3 * 30 * 24h`).
- Server time is UTC.
- Public verification key: available from the SSO login/profile page `Token Details` modal and from `runtime-config.json` under `ssoJwt.public_key_pem`.

Claims:

| Claim          | Type   | Required | Description                                  |
| -------------- | ------ | -------- | -------------------------------------------- |
| `iss`          | string | yes      | Token issuer. Current value is `laisky-sso`. |
| `sub`          | string | yes      | Stable external user UID in UUID format.     |
| `uid`          | string | yes      | Stable external user UID in UUID format.     |
| `iat`          | number | yes      | Issued-at time in Unix seconds, UTC.         |
| `exp`          | number | yes      | Expiration time in Unix seconds, UTC.        |
| `jti`          | string | yes      | Token ID.                                    |
| `username`     | string | yes      | User account identifier.                     |
| `display_name` | string | yes      | User display name.                           |

Decoded payload example:

```json
{
  "iss": "laisky-sso",
  "sub": "0194d5f8-19f7-7f7b-a8d3-421a60f8d8ab",
  "uid": "0194d5f8-19f7-7f7b-a8d3-421a60f8d8ab",
  "iat": 1738929600,
  "exp": 1746705600,
  "jti": "0194d5f8-19f7-7f7b-a8d3-421a60f8d8ab",
  "username": "alice@example.com",
  "display_name": "Alice"
}
```

## Token Validation

### Recommended: GraphQL `WhoAmI`

Send the token as a Bearer token to the SSO GraphQL endpoint:

- Endpoint: `https://sso.laisky.com/query`
- Header: `Authorization: Bearer <sso_token>`

```graphql
query {
  WhoAmI {
    id
    username
  }
}
```

Example:

```bash
curl -X POST 'https://sso.laisky.com/query' \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer <sso_token>' \
  -d '{"query":"query { WhoAmI { id username } }"}'
```

If successful, treat the token as valid and map the returned user to your local session model.

Treat any GraphQL error as an invalid session, and a transport error, timeout,
non-200 status or malformed body as "SSO unavailable" (fail closed, but do not
tell the user their session expired). Use a fixed HTTPS endpoint, do not follow
redirects with the bearer token, and authorize by comparing the returned `id`
with the UIDs your application allows; a valid SSO session alone does not mean
the user may act in your application.

The SSO login page keeps the issued session on the SSO origin before handing it
to your callback, so a later visit to SSO from any client application reuses it
without asking for credentials again.

`WhoAmI.id` is the same stable external UID carried in `sso_token.sub` and `sso_token.uid`.

### Optional: Local JWT Verification

Local verification uses the public Ed25519 key published by SSO. You do not need access to the private signing key.

Minimum checks:

1. Verify the `EdDSA` signature with the published public key.
2. Verify `exp` and `iat`.
3. Verify `iss == "laisky-sso"`.
4. Verify `sub` and `uid` have the expected UUID format and match each other.
5. Reject tokens whose JWT header algorithm is not `EdDSA`.

The login page and authenticated profile page include a `Token Details` button. Open it to view:

- Token type, algorithm, issuer, and TTL.
- PEM-encoded public key.
- JSON schema for expected token claims.

On the profile page, the same modal also shows the current user's active `sso_token` and decoded header/payload JSON.

## Account and Profile Flows

### Email Registration

Users can register from the SSO login page with:

- Account email.
- Display name.
- Password.
- Six-digit email verification code.

Registration starts by requesting a code with `UserRequestEmailCode` and purpose `register`. The server stores only a hashed code, sends the code through SMTP, and expires it after 30 minutes. `UserRegister` must include the code; successful registration consumes it.

New email registrations are active immediately and can sign in after registration succeeds.

### Email Code Login

Users can sign in with a one-time email verification code instead of a password.

Server flow:

1. `UserRequestEmailCode` with purpose `login` validates Turnstile when the client is high risk, creates a six-digit code for active existing users, and sends it through SMTP.
2. The code is valid for 30 minutes.
3. `UserLoginWithEmailCode` validates Turnstile when required, consumes the code, and returns `sso_token`.

The login code request returns a generic success message even when the account cannot use this flow, so clients should not use the response to infer account existence.

### GitHub Registration and Login

Users can select GitHub from the SSO login page.

Server flow:

1. `UserGithubOAuthStart` validates Turnstile when configured, validates `redirect_to`, signs state, and returns GitHub `authorize_url`.
2. GitHub redirects back to `/github/callback`.
3. `UserGithubOAuthLogin` verifies signed state, exchanges the code, loads the verified GitHub email, and finds or creates the local user.
4. SSO issues `sso_token` and redirects to the original validated target.

The server stores the GitHub provider subject and verified email. It does not store GitHub access tokens.

### GitHub Profile Binding

Authenticated users can bind GitHub from the SSO profile page.

Server flow:

1. The profile page calls `UserGithubOAuthBindStart` with the current `Authorization: Bearer <sso_token>` header.
2. The server validates the current user, signs OAuth state with the user's external UID and `bind` mode, and returns GitHub `authorize_url`.
3. GitHub redirects back to the same `/github/callback` route used by login.
4. `UserGithubOAuthLogin` verifies signed state, exchanges the code, loads the verified GitHub email, and binds the GitHub provider subject to the authenticated user from the signed state.
5. SSO issues a refreshed `sso_token` and redirects back to the profile page.

Binding fails if the GitHub identity is already bound to a different local user. It is idempotent when the same GitHub identity is already bound to the same local user.

### Password and TOTP Login

Users can sign in with account and password.

If TOTP is enabled on the account, the login must include a valid TOTP code. A password-only attempt for a TOTP-enabled user is rejected.

### Passkey Login

Users can select passkey login from the SSO login page.

The server uses discoverable WebAuthn credentials:

1. `UserStartPasskeyLogin` validates Turnstile when configured and returns browser credential request options plus a signed session.
2. The browser calls `navigator.credentials.get`.
3. `UserFinishPasskeyLogin` verifies the signed session and WebAuthn assertion, updates the credential counter, and returns `sso_token`.

Each signed login session can be finished once; resubmitting the same
session is rejected even when the authenticator reports a zero signature
counter, as synced platform passkeys do. A non-zero counter that does not
advance is rejected as a possible replay or cloned authenticator. Consumed
sessions are tracked in process memory until they expire, so a multi-replica
deployment must send a ceremony's start and finish to the same instance.

### Profile Page

Standalone SSO profile route:

```text
https://sso.laisky.com/profile
```

MCP-mounted SSO profile route:

```text
https://mcp.laisky.com/sso/profile
```

Profile supports:

- Viewing the stable external UID used by clients and JWTs.
- Viewing SSO JWT metadata, the public verification key, claims schema, the current session token, and decoded token JSON.
- Password change.
- GitHub OAuth identity binding.
- TOTP enrollment with a scannable QR code, plus manual secret/URI fallback and disable.
- Passkey registration, rename, and delete.
- Viewing registered passkey names, creation times, and shortened credential IDs.
- Viewing whether GitHub is bound and how many passkeys are registered.

## Data and Migration Notes

- `settings.web.sso.user_store` selects one authoritative backend. `oneapi` is
  the production target; `mongo` exists only for a controlled migration or
  rollback window. The service never falls back automatically after OneAPI has
  been selected.
- OneAPI's `users.uuid` remains its own public identifier. The SSO-owned
  `sso_user_links` table preserves the existing SSO UUID and Mongo-shaped blog
  author ID, so JWT subjects and historical post ownership do not change at
  cutover.
- New OneAPI users use their OneAPI UUID as the SSO UID and receive a stable,
  deterministic blog author ObjectID. Existing users must be linked before
  cutover so their prior SSO UID and author ObjectID are retained.
- Passwords are OneAPI-compatible bcrypt hashes. Legacy Mongo password hashes
  cannot be copied; affected users must use email-code login and set a new
  password, or complete a separately audited login-time upgrade.
- SSO uses OneAPI's native `passkey_credentials` and confirmed
  `users.totp_secret` fields. Pending TOTP enrollment and hashed email codes
  remain in SSO-owned SQL tables.
- `settings.web.sso_jwt.private_key` signs SSO JWTs when configured. If it is omitted, the service derives an Ed25519 signing key from `settings.secret` for compatibility with older deployments.
- `settings.secret` signs GitHub OAuth state and passkey sessions. When the SSO JWT private key is omitted, rotating `settings.secret` also invalidates active SSO tokens.
- Passkey user handles use the external SSO UID. Passkeys registered before
  the UID switch carry the account's hex blog ObjectID as their user handle.
  Login resolves the owner from the credential ID first and then accepts either
  that owner's UID or that owner's legacy ObjectID handle (on OneAPI, the
  imported `sso_user_links.blog_object_id`). Any other handle is rejected; a
  supplied handle is never trusted on its own.
- MongoDB store compatibility: blog accounts created before the `status` field
  existed have no status and remain active. An explicit non-active status (for
  example `pending`) is rejected by every login method and by token
  validation. Existing MongoDB accounts are looked up with the original
  128-character account bound and their passwords verified with the original
  1024-character bound (the GraphQL login input is still capped at 100). New
  passwords keep the 8–20 character OneAPI-compatible policy.

Follow the [OneAPI SSO migration runbook](sso_oneapi_migration.md) before
changing the production selector.

## Admin Configuration

### SSO Redirect Policy and Client Registry

```yaml
settings:
  web:
    sso:
      # allow (default) keeps the legacy sso_token handoff for unregistered targets;
      # deny refuses them. Blog, registered clients and pages on the issuer origin that
      # completes sign-in are unaffected.
      legacy_token_redirect: allow
      clients:
        accounting: # client ID
          redirect_uris:
            - https://accounting.laisky.com/api/auth/sso/callback
        cv:
          redirect_uris:
            - https://cv.laisky.com/cv/sso/callback
          # Migration aid only: the client's hostname keeps the legacy handoff while its
          # consumer ships the code flow. Omit it (default deny) to finish the cutover.
          legacy_token_redirect: allow
```

Startup validation rejects an unknown `legacy_token_redirect`, an invalid client ID, a
non-canonical or disallowed callback, unknown client fields and a callback registered by
two clients. Changes take effect after a restart. The registry is public: it is published
in `runtime-config.json` so the login page can recognize code requests.

### Authoritative OneAPI User Database

```yaml
settings:
  db:
    oneapi:
      driver: postgres # postgres, mysql, or sqlite for local development
      dsn: postgres://oneapi:REPLACE_ME@db.example.com:5432/oneapi
      max_idle_conns: 5
      max_open_conns: 20
      conn_max_lifetime_seconds: 300
  web:
    sso:
      user_store: oneapi
```

For SQLite development, set `driver: sqlite` and `sqlite_path`; do not set a
DSN. Sharing a SQLite file between independently deployed OneAPI and SSO
processes is not recommended for production. Startup validates the OneAPI-owned
`users`, `tokens`, `options`, and `passkey_credentials` schemas, migrates only
SSO-owned side tables, and fails closed if the selected database is unavailable.
The DSN is sensitive and is never logged.

### Site Routing and Branding

Configure SSO as a site under `settings.web.sites`.

```yaml
settings:
  web:
    sites:
      sso:
        host: sso.laisky.com
        title: Laisky SSO
        favicon: https://s3.laisky.com/uploads/2025/12/sso_icon.png
        theme: sso
        router: sso
        public_base_path: /
```

Use `router: sso` for the standalone SSO experience. The MCP router can still expose `/sso/login`, `/sso/profile`, and `/sso/github/callback`.

### SSO JWT Signing Key

Production deployments should configure an explicit PKCS#8 Ed25519 private key:

```yaml
settings:
  web:
    sso_jwt:
      private_key: |
        -----BEGIN PRIVATE KEY-----
        YOUR_BASE64_PKCS8_ED25519_PRIVATE_KEY
        -----END PRIVATE KEY-----
```

The corresponding public key is exposed to clients through `runtime-config.json` and the login/profile page `Token Details` modal. Rotate this key deliberately because existing SSO tokens signed by the previous key stop validating after rotation.

### GitHub OAuth

Configure a GitHub OAuth App with callback URL:

```text
https://sso.laisky.com/github/callback
```

Configuration:

```yaml
settings:
  web:
    github_oauth:
      client_id: YOUR_GITHUB_OAUTH_CLIENT_ID
      client_secret: YOUR_GITHUB_OAUTH_CLIENT_SECRET
      redirect_url: https://sso.laisky.com/github/callback
```

The server requests `read:user` and `user:email` scopes so it can load a verified email address.

### SMTP Email Codes

Email registration and email-code login require an SMTP relay:

```yaml
settings:
  web:
    smtp:
      host: smtp.example.com # SMTP server domain
      port: 465 # implicit TLS port; optional, default 465
      username: sso@example.com # login user, also used as the From address
      password: YOUR_SMTP_PASSWORD
      sender_name: Laisky SSO # optional From display name
```

`port` is optional and defaults to `465`, which connects over implicit TLS (SSL on connect). `username` doubles as the From address, and `sender_name` sets the optional From display name. Any SMTP provider works, including Mailgun's SMTP credentials (`smtp.mailgun.org`). Codes are six digits, expire after 30 minutes, and are stored only as HMAC hashes.

### WebAuthn Passkeys

Passkeys require a stable relying-party origin and ID. The server derives these from the request by default.

Optional explicit configuration:

```yaml
settings:
  web:
    webauthn:
      origin: https://sso.laisky.com
      rp_id: sso.laisky.com
      rp_display_name: Laisky SSO
```

Use explicit values in production when the service is behind a proxy or when the incoming request host does not exactly match the public SSO origin.

### Cloudflare Turnstile

Turnstile is optional. Once a secret key is configured anywhere, SSO registration and login flows may require a valid Turnstile token after frequent requests, repeated failures, or other suspicious activity.

Per-site configuration:

```yaml
settings:
  web:
    sites:
      sso:
        host: sso.laisky.com
        router: sso
        default: true # the pair for hosts that match no site with its own pair
        turnstile_site_key: YOUR_CLOUDFLARE_TURNSTILE_SITE_KEY
        turnstile_secret_key: YOUR_CLOUDFLARE_TURNSTILE_SECRET_KEY
```

Global fallback:

```yaml
settings:
  web:
    turnstile:
      site_key: YOUR_CLOUDFLARE_TURNSTILE_SITE_KEY # optional; without it no widget can render for this pair
      secret_key: YOUR_CLOUDFLARE_TURNSTILE_SECRET_KEY
```

Pair selection. The widget key and the secret always come from the same
place, so a token from the widget the page renders verifies against the secret
the server checks. The request host (the `Host` header, or `X-Forwarded-Host`
from a [trusted proxy](client_identity.md#forwarded-host-and-scheme)) chooses
the pair in this order:

1. the site whose `host`/`hosts` match, if it has a `turnstile_secret_key`;
2. the site marked `default: true`, if it has one;
3. the global `settings.web.turnstile` pair;
4. the only site with a secret.

The request host can choose which configured pair applies, but never whether
one does. Once any secret exists, every host is gated. Startup fails when
several sites have secrets but none is `default` and no global secret is set,
because the pair for other hosts would then be ambiguous.

`/runtime-config.json` serves the selected pair's widget key for the requesting
host. A host whose own site has no pair (such as an MCP site) therefore renders
the default pair's widget. The default widget's hostname list in the Cloudflare
dashboard must include every host that falls back to it. Adding the apex domain
authorizes all its subdomains; an unlisted host shows Turnstile error `110200`.

Turnstile is enforced on:

- Email registration code request through `UserRequestEmailCode` with purpose `register` when the client is high risk.
- Email registration completion through `UserRegister` when the client is high risk.
- Password login through `UserLogin` when the client or the account is high risk.
- Email-code request and login through `UserRequestEmailCode` with purpose `login` and `UserLoginWithEmailCode` when the client or the account is high risk.
- GitHub OAuth start through `UserGithubOAuthStart`.
- Passkey login start through `UserStartPasskeyLogin`.

`UserGithubOAuthBindStart` is authenticated by the current SSO token and does not use Turnstile.

Risk policy:

- A client is high risk after either of these:
  - more than `frequency_threshold` (default 5) auth requests within
    `frequency_window_seconds` (default 60);
  - `failure_threshold` (default 3) credential failures within
    `failure_window_seconds` (default 900).
- The client key is the client address resolved by the
  [client identity policy](client_identity.md). Forwarding headers count only
  from trusted proxies, and IPv6 clients are grouped by /64.
- An account is high risk after `account_failure_threshold` credential failures
  within `failure_window_seconds`, counted across every client. The threshold
  defaults to `failure_threshold`. Failures count from every flow that names an
  account:
  - `UserLogin` and `BlogLogin`, including the TOTP step;
  - `UserLoginWithEmailCode`;
  - `UserRequestEmailCode` with purpose `login` names the account but has no
    credential check.

  Rotating client addresses therefore cannot keep an attacked account below the
  threshold. Registration, GitHub OAuth start and passkey start name no account
  and are keyed by client only.
- Requests that are admitted but whose credential check has not finished count
  toward the account threshold. Concurrent guesses therefore cannot all slip in
  before the first failure is recorded. A flood of concurrent legitimate logins
  to one account may also see a challenge.
- Accounts are keyed two ways.
  - **By folded spelling.** This is the Unicode collation key at primary
    strength: case-, accent- and width-insensitive, with default-ignorable
    characters dropped. It matches what MySQL's `utf8mb4_0900_ai_ci` treats as
    equal, so `Alice`, `alicé` and `ａｌｉｃｅ` share one budget even for unknown
    accounts. Folding more than a store does only makes a challenge appear
    sooner.
  - **By store identity.** Once a failure reveals which account a spelling
    resolves to (wrong password, wrong TOTP, inactive account), the failure also
    counts against that account's identity: the OneAPI user ID or MongoDB
    document ID. A username and an email of one OneAPI user then share a budget.
  
  The identity comes from the credential check that already runs, so there is
  no extra lookup. Clients always see the same `invalid credentials` error, so
  existence is not revealed beyond what the challenge behavior itself implies.
- The account signal demands a challenge only where the client can present one.
  The entry point must accept a token, and the host's pair must have a widget
  key. The deprecated `BlogLogin` accepts no token, and a pair without a site
  key renders no widget. There only the client signal applies, so a third party
  cannot keep an owner out. Such gaps are logged as a warning at most once a
  minute, with a count of the suppressed ones.
- The policy only ever asks for a Turnstile challenge. It never locks an account
  out. Once the challenge is solved, the request proceeds.
- A fully successful sign-in resets the failure counts of that client, that
  spelling and that account identity.
- Only rejections caused by the submitted credentials count as failures. A
  database or signing outage returns `login_unavailable` and never counts,
  against either the client or the account. Its in-flight reservation is still
  released.
- Solving a challenge earns the client a short allowance: its next
  `clearance_uses` (default 3) auth requests within `clearance_seconds`
  (default 300) are not challenged again.
  - The TOTP step after a verified password therefore needs no second challenge,
    even for an attacked account.
  - The allowance admits one request at a time. While the request that solved
    the challenge, or one admitted by the allowance, is still in flight, other
    requests must solve their own challenge.
  - Any credential failure revokes the allowance immediately.

  Each further guess against a high-risk account therefore needs a fresh
  challenge, even in parallel.
- A supplied token that Cloudflare does not accept returns `turnstile_failed`.
  The client must show a fresh challenge. It is never reported as invalid
  credentials.
- The tracker is in memory and bounded:
  - at most `max_tracked_clients` (default 10000) clients and
    `max_tracked_accounts` (default 10000) account records (spellings and
    identities);
  - records expire with their windows;
  - when the account table is full, accounts below their threshold are evicted
    first (fewest recent failures, then fewest failures overall, then the
    longest idle);
  - accounts at or above their threshold, and accounts with requests in flight,
    are never evicted. If only such accounts remain, the table saturates, and
    until records expire every account it cannot track is treated as high risk
    (challenged, never locked out). A flood therefore cannot push out an account
    under attack;
  - accounts are stored only as HMAC-SHA256 keyed hashes, with a random
    per-process key.

```yaml
settings:
  web:
    turnstile:
      risk:
        frequency_window_seconds: 60
        frequency_threshold: 5
        failure_window_seconds: 900
        failure_threshold: 3
        account_failure_threshold: 3 # defaults to failure_threshold
        clearance_seconds: 300
        clearance_uses: 3
        max_tracked_clients: 10000
        max_tracked_accounts: 10000
```

The deprecated `BlogLogin` mutation cannot carry a Turnstile token. When the client itself is high risk, that legacy path fails closed and clients must use `UserLogin`. An account flagged by failures from other clients does not block it (see above); its failures still count toward the account for every other flow.

## GraphQL API Reference

### Queries

```graphql
query {
  WhoAmI {
    id
    username
  }
}
```

`WhoAmI.id` is the external UID.

```graphql
query {
  UserProfile {
    uid
    account
    auth_methods
    password_enabled
    totp_enabled
    passkey_count
    passkeys {
      id
      name
      created_at
    }
    github_bound
    user {
      id
      username
    }
  }
}
```

### Email Registration

```graphql
mutation RequestRegisterCode($account: String!, $turnstileToken: String) {
  UserRequestEmailCode(account: $account, purpose: "register", turnstile_token: $turnstileToken) {
    msg
  }
}
```

```graphql
mutation Register($account: String!, $password: String!, $displayName: String!, $emailCode: String!, $turnstileToken: String) {
  UserRegister(
    account: $account
    password: $password
    display_name: $displayName
    captcha: ""
    turnstile_token: $turnstileToken
    email_code: $emailCode
  ) {
    msg
  }
}
```

### Password Login

```graphql
mutation Login($account: String!, $password: String!, $turnstileToken: String, $totpCode: String) {
  UserLogin(account: $account, password: $password, turnstile_token: $turnstileToken, totp_code: $totpCode) {
    token
    user {
      id
      username
    }
  }
}
```

### Email Code Login

```graphql
mutation RequestLoginCode($account: String!, $turnstileToken: String) {
  UserRequestEmailCode(account: $account, purpose: "login", turnstile_token: $turnstileToken) {
    msg
  }
}
```

```graphql
mutation LoginWithEmailCode($account: String!, $emailCode: String!, $turnstileToken: String) {
  UserLoginWithEmailCode(account: $account, email_code: $emailCode, turnstile_token: $turnstileToken) {
    token
    user {
      id
      username
    }
  }
}
```

### GitHub OAuth

```graphql
mutation StartGithub($redirectTo: String, $turnstileToken: String) {
  UserGithubOAuthStart(redirect_to: $redirectTo, turnstile_token: $turnstileToken) {
    authorize_url
  }
}
```

```graphql
mutation FinishGithub($code: String!, $state: String!) {
  UserGithubOAuthLogin(code: $code, state: $state) {
    token
    redirect_to
    user {
      id
      username
    }
  }
}
```

```graphql
mutation StartGithubBind {
  UserGithubOAuthBindStart {
    authorize_url
  }
}
```

Call `UserGithubOAuthBindStart` with `Authorization: Bearer <sso_token>`.

### TOTP

```graphql
mutation {
  UserStartTOTPSetup {
    secret
    provisioning_uri
  }
}
```

```graphql
mutation ConfirmTOTP($code: String!) {
  UserConfirmTOTPSetup(code: $code) {
    totp_enabled
  }
}
```

```graphql
mutation DisableTOTP($currentPassword: String!) {
  UserDisableTOTP(current_password: $currentPassword) {
    totp_enabled
  }
}
```

### Passkeys

```graphql
mutation StartPasskeyRegistration($label: String!) {
  UserStartPasskeyRegistration(label: $label) {
    options_json
    session
  }
}
```

```graphql
mutation FinishPasskeyRegistration($label: String!, $session: String!, $credentialJSON: String!) {
  UserFinishPasskeyRegistration(label: $label, session: $session, credential_json: $credentialJSON) {
    passkey_count
    passkeys {
      id
      name
      created_at
    }
  }
}
```

```graphql
mutation RenamePasskey($passkeyID: String!, $name: String!) {
  UserRenamePasskey(passkey_id: $passkeyID, name: $name) {
    passkey_count
    passkeys {
      id
      name
      created_at
    }
  }
}
```

```graphql
mutation DeletePasskey($passkeyID: String!) {
  UserDeletePasskey(passkey_id: $passkeyID) {
    passkey_count
    passkeys {
      id
      name
      created_at
    }
  }
}
```

```graphql
mutation StartPasskeyLogin($redirectTo: String, $turnstileToken: String) {
  UserStartPasskeyLogin(redirect_to: $redirectTo, turnstile_token: $turnstileToken) {
    options_json
    session
  }
}
```

```graphql
mutation FinishPasskeyLogin($session: String!, $credentialJSON: String!) {
  UserFinishPasskeyLogin(session: $session, credential_json: $credentialJSON) {
    token
    redirect_to
    user {
      id
      username
    }
  }
}
```

## Security Notes

1. Always use HTTPS in production. The issuer refuses plain `http` targets on production hosts.
2. Always validate your own `state` in callback handlers.
3. Treat `sso_token` as a credential and never log full token values.
4. Remove `sso_token` from URLs immediately after callback processing. Prefer the
   registered client code flow, which never puts the bearer in a URL.
5. Use HttpOnly, Secure, SameSite cookies for local application sessions.
6. Keep GitHub OAuth client secrets, SMTP passwords, and Turnstile secret keys out of frontend bundles.
7. Passkey sessions and GitHub OAuth states are signed by the server and expire after a short window.
8. Store passkey credential records as sensitive data; avoid exposing credential JSON in logs or APIs.
9. SSO JWTs are signed, not encrypted; anyone holding a token can decode its claims.

## Common Failure Cases

1. `Missing redirect_to parameter`: the password login redirect flow needs a callback target.
2. `Unsupported redirect protocol`: callback URL is not an absolute `https`/`http` URL or a `/` path.
3. `Unsupported redirect host`: callback host is outside the SSO allow-list or uses a non-canonical spelling.
4. `Insecure redirect target`: a production host was given with `http://`; use `https://`.
5. `This application must use the SSO code flow`: the hostname belongs to a registered client; send a code request.
6. `This application is not registered for SSO`: `legacy_token_redirect` is `deny` and the client is not registered.
7. `Invalid SSO code request` or `Unknown SSO client`: the code request does not match a registration exactly.
8. `SSO configuration could not be loaded`: the login page could not read `runtime-config.json`; reload.
9. `Unsupported development target`: a production issuer was asked to hand a session to an unregistered private or CGNAT address; use `localhost` or register the exact callback ([Development callbacks](#development-callbacks)).
10. `turnstile token is required`: Turnstile is configured but the client did not provide a token.
11. GitHub login returns no verified email: the GitHub account has no verified email visible through the granted scopes.
12. GitHub binding fails because the GitHub identity is already bound to another SSO user.
13. Passkey registration fails on HTTP: WebAuthn requires a secure context, except for browser-supported local development cases.
14. `WhoAmI` returns unauthorized: token is missing, malformed, expired, or not sent as `Authorization: Bearer <token>`.
15. Callback loops after success: local callback did not persist a session before removing `sso_token`.
16. `turnstile_required` / `turnstile_failed`: show (or re-show) the Turnstile widget and retry with a fresh token.
17. `login_unavailable`: the credentials could not be evaluated because a backing dependency failed; retry later. It does not mean the password is wrong.
18. `totp_required`: the password was accepted; prompt for the TOTP code and resubmit.

## Go-Live Checklist

1. SSO site routing and public base path are configured.
2. The client is registered in `settings.web.sso.clients` and uses the code flow; its callback passes `redirect_to` validation over https.
3. Callback handler validates `state` and the PKCE verifier is redeemed once at `/sso/token`.
4. Legacy clients only: callback handler validates `sso_token` with `WhoAmI` or approved local verification.
5. Callback handler removes `sso_code`, `sso_state` and any `sso_token` from the browser URL.
6. GitHub OAuth callback URL exactly matches the deployed SSO callback route.
7. WebAuthn `origin` and `rp_id` are stable for the deployed hostname.
8. Turnstile site key and secret key are configured together when Turnstile is required.
9. `settings.web.trusted_proxies` matches the ingress, every vhost sets `X-Forwarded-For` and `X-Real-IP`, and origin access is restricted to the CDN ([client identity](client_identity.md#ingress-requirements-operator-action)).
10. SSO JWT private key is explicitly configured and the published public key is available from the login/profile page modal.
11. User `uid` is indexed uniquely and downstream systems use UID instead of database IDs.
12. Logs and monitoring do not expose JWTs, OAuth codes, email verification codes, Turnstile tokens, TOTP secrets, passwords, SMTP passwords, or passkey credential JSON.
