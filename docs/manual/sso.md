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
  - [Callback Contract](#callback-contract)
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
- Operators configuring SSO login methods, GitHub OAuth, WebAuthn passkeys, and Cloudflare Turnstile.

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

- Email account registration with password.
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

1. Your application redirects the browser to the SSO login page with a validated callback URL in `redirect_to`.
2. The user signs in with one of the enabled methods:
   - Password, plus TOTP when the user has TOTP enabled.
   - Passkey.
   - GitHub.
3. SSO redirects the browser to your callback URL with `sso_token=<JWT>`.
4. Your backend validates `sso_token`.
5. Your backend creates its own local session and redirects the browser to a clean URL without `sso_token`.

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

The SSO frontend and backend accept only these redirect targets:

- Protocol: `http` or `https`.
- Host:
  - `laisky.com`.
  - Any `*.laisky.com` subdomain.
  - Internal IP ranges:
    - IPv4: `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`, `127.0.0.0/8`, `100.64.0.0/10`.
    - IPv6: `::1`, `fc00::/7`.

If your callback host is outside this policy, update the allow-list in code before release.

## Callback Contract

After successful login, SSO redirects to `redirect_to` and appends:

```text
sso_token=<JWT>
```

Behavior:

- Existing query parameters are preserved.
- Existing `sso_token` is overwritten.
- GitHub and passkey login use a signed server state/session before redirecting, so the callback target is not trusted from the browser at finish time.

Your callback endpoint should:

1. Validate your own `state`.
2. Read `sso_token`.
3. Validate `sso_token`.
4. Create your own session, preferably with an HttpOnly secure cookie.
5. Redirect to a clean URL without `sso_token`.

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

New email registrations are active immediately and can sign in after registration succeeds.

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

- New users receive `uid` when the user record is created.
- Existing users that do not yet have `uid` are assigned one lazily when loaded through authenticated SSO paths.
- `settings.web.sso_jwt.private_key` signs SSO JWTs when configured. If it is omitted, the service derives an Ed25519 signing key from `settings.secret` for compatibility with older deployments.
- `settings.secret` signs GitHub OAuth state and passkey sessions. When the SSO JWT private key is omitted, rotating `settings.secret` also invalidates active SSO tokens.
- The users collection should have a unique sparse index on `uid` so old records without `uid` can coexist while migration happens lazily.
- Passkey user handles use the external UID for new registrations. Legacy passkey handles based on internal IDs are still accepted during login for backward compatibility.

## Admin Configuration

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

Turnstile is optional. If a matching secret key is configured, SSO registration and login flows require a valid Turnstile token.

Per-site configuration:

```yaml
settings:
  web:
    sites:
      sso:
        host: sso.laisky.com
        router: sso
        turnstile_site_key: YOUR_CLOUDFLARE_TURNSTILE_SITE_KEY
        turnstile_secret_key: YOUR_CLOUDFLARE_TURNSTILE_SECRET_KEY
```

Global fallback:

```yaml
settings:
  web:
    turnstile:
      secret_key: YOUR_CLOUDFLARE_TURNSTILE_SECRET_KEY
```

Turnstile is enforced on:

- Email registration.
- Password login through `UserLogin`.
- GitHub OAuth start through `UserGithubOAuthStart`.
- Passkey login start through `UserStartPasskeyLogin`.

`UserGithubOAuthBindStart` is authenticated by the current SSO token and does not use Turnstile.

The deprecated `BlogLogin` mutation cannot carry a Turnstile token. When Turnstile is configured, that legacy path fails closed and clients must use `UserLogin`.

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
mutation Register($account: String!, $password: String!, $displayName: String!, $turnstileToken: String) {
  UserRegister(account: $account, password: $password, display_name: $displayName, captcha: "", turnstile_token: $turnstileToken) {
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

1. Always use HTTPS in production.
2. Always validate your own `state` in callback handlers.
3. Treat `sso_token` as a credential and never log full token values.
4. Remove `sso_token` from URLs immediately after callback processing.
5. Use HttpOnly, Secure, SameSite cookies for local application sessions.
6. Keep GitHub OAuth client secrets and Turnstile secret keys out of frontend bundles.
7. Passkey sessions and GitHub OAuth states are signed by the server and expire after a short window.
8. Store passkey credential records as sensitive data; avoid exposing credential JSON in logs or APIs.
9. SSO JWTs are signed, not encrypted; anyone holding a token can decode its claims.

## Common Failure Cases

1. `Missing redirect_to parameter`: the password login redirect flow needs a callback target.
2. `Unsupported redirect protocol`: callback URL is not `http` or `https`.
3. `Unsupported redirect host`: callback host is outside the SSO allow-list.
4. `turnstile token is required`: Turnstile is configured but the client did not provide a token.
5. GitHub login returns no verified email: the GitHub account has no verified email visible through the granted scopes.
6. GitHub binding fails because the GitHub identity is already bound to another SSO user.
7. Passkey registration fails on HTTP: WebAuthn requires a secure context, except for browser-supported local development cases.
8. `WhoAmI` returns unauthorized: token is missing, malformed, expired, or not sent as `Authorization: Bearer <token>`.
9. Callback loops after success: local callback did not persist a session before removing `sso_token`.

## Go-Live Checklist

1. SSO site routing and public base path are configured.
2. Callback URL passes `redirect_to` validation.
3. Callback handler validates `state`.
4. Callback handler validates `sso_token` with `WhoAmI` or approved local verification.
5. Callback handler removes `sso_token` from the browser URL.
6. GitHub OAuth callback URL exactly matches the deployed SSO callback route.
7. WebAuthn `origin` and `rp_id` are stable for the deployed hostname.
8. Turnstile site key and secret key are configured together when Turnstile is required.
9. SSO JWT private key is explicitly configured and the published public key is available from the login/profile page modal.
10. User `uid` is indexed uniquely and downstream systems use UID instead of database IDs.
11. Logs and monitoring do not expose JWTs, OAuth codes, Turnstile tokens, TOTP secrets, passwords, or passkey credential JSON.
