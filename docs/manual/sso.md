# SSO Integration Manual

This manual explains how external engineering teams can integrate this SSO system as a login method for their own applications.

## Menu

- [SSO Integration Manual](#sso-integration-manual)
  - [Menu](#menu)
  - [Overview](#overview)
  - [Integration Modes](#integration-modes)
  - [Hosted Login Flow (Recommended)](#hosted-login-flow-recommended)
  - [Detailed Flow](#detailed-flow)
    - [1. Build the SSO login URL](#1-build-the-sso-login-url)
    - [2. Redirect allowlist rules](#2-redirect-allowlist-rules)
    - [3. Receive token on callback](#3-receive-token-on-callback)
  - [Callback Contract](#callback-contract)
  - [Token Validation](#token-validation)
    - [Strategy A: Remote introspection through GraphQL `WhoAmI`](#strategy-a-remote-introspection-through-graphql-whoami)
    - [Strategy B: Local JWT verification](#strategy-b-local-jwt-verification)
  - [Custom Login UI via GraphQL (Advanced)](#custom-login-ui-via-graphql-advanced)
  - [Turnstile Configuration](#turnstile-configuration)
  - [Security Checklist](#security-checklist)
  - [Troubleshooting](#troubleshooting)
  - [Appendix: API Examples](#appendix-api-examples)
    - [A. Redirect user to SSO](#a-redirect-user-to-sso)
    - [B. Validate token with `WhoAmI`](#b-validate-token-with-whoami)
    - [C. Custom UI login via GraphQL](#c-custom-ui-login-via-graphql)

## Overview

The SSO service authenticates users and redirects them back to your system with an SSO token in the query string.

- Login page path: `/sso/login`.
- On an SSO-only host/router, `/` also serves the same login page.
- After successful login, users are redirected to your callback URL with `sso_token=<JWT>` appended.

## Integration Modes

1. Hosted login page (recommended)

- Your app redirects users to the SSO page.
- SSO handles credential input and optional Turnstile challenge.
- Your callback endpoint receives `sso_token`.

2. Custom login UI (advanced)

- Your frontend calls GraphQL `UserLogin` directly.
- You must handle Turnstile token collection when enabled.

## Hosted Login Flow (Recommended)

Use this as the default integration path.

1. User clicks "Login with Laisky SSO" in your app.
2. Your app redirects to:

- `https://sso.laisky.com/sso/login?redirect_to=<urlencoded callback URL>`

3. User authenticates in SSO.
4. SSO redirects to your callback URL and appends:

- `sso_token=<JWT>`

5. Your backend validates the token and creates your local session.
6. Your app removes the token from browser URL immediately.

## Detailed Flow

### 1. Build the SSO login URL

Construct:

```text
{SSO_BASE_URL}/sso/login?redirect_to={URL_ENCODED_CALLBACK}
```

Example:

```text
https://sso.laisky.com/sso/login?redirect_to=https%3A%2F%2Fapp.laisky.com%2Fauth%2Fsso%2Fcallback%3Fstate%3Dabc123
```

### 2. Redirect allowlist rules

`redirect_to` is validated by SSO.

Allowed protocols:

- `http`
- `https`

Allowed hosts:

- `laisky.com` and subdomains like `*.laisky.com`
- Internal IP ranges:
  - IPv4: `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`, `127.0.0.0/8`, `100.64.0.0/10`
  - IPv6: loopback `::1` and ULA `fc00::/7`

If your callback host is outside this policy, coordinate with the SSO team before go-live.

### 3. Receive token on callback

SSO redirects to:

```text
https://your-app.example.com/auth/sso/callback?...&sso_token=<JWT>
```

Your callback endpoint should:

1. Read `sso_token`.
2. Validate token (see Token Validation).
3. Bind token identity to a local account/session.
4. Redirect to a clean URL without `sso_token`.

## Callback Contract

Incoming query parameters to your callback typically include:

- Existing parameters you set in `redirect_to` (for example `state`).
- `sso_token` added by SSO.

Recommended behavior:

1. Validate your `state` parameter.
2. Validate `sso_token`.
3. Create your own session cookie.
4. Redirect to a clean page (for example `/dashboard`) to avoid token leakage.

## Token Validation

The SSO token is a JWT signed with HS256.

Expected claims include:

- `iss`: `laisky-sso`
- `sub`: internal user ID (hex string)
- `username`: account name
- `display_name`: display name
- Standard JWT time claims (`iat`, `exp`)

Token lifetime is currently about 90 days.

Use one of these validation strategies.

### Strategy A: Remote introspection through GraphQL `WhoAmI`

Send the token as Bearer token to SSO GraphQL:

- Endpoint: `{SSO_BASE_URL}/query`
- Header: `Authorization: Bearer <sso_token>`
- Query:

```graphql
query {
  WhoAmI {
    id
    username
  }
}
```

If query succeeds, treat token as valid and map the user.

### Strategy B: Local JWT verification

If your system is allowed to share the SSO signing secret (`settings.secret`), you can verify JWT locally with HS256.

Validation requirements:

- Verify signature with shared secret.
- Verify `exp` and `iat`.
- Verify issuer equals `laisky-sso`.

## Custom Login UI via GraphQL (Advanced)

Use this only if you cannot use the hosted login page.

GraphQL mutation:

```graphql
mutation SsoLogin($account: String!, $password: String!, $turnstileToken: String) {
  UserLogin(account: $account, password: $password, turnstile_token: $turnstileToken) {
    token
    user {
      id
      username
    }
  }
}
```

Notes:

- If Turnstile secret is configured for the target site, `turnstile_token` is required.
- If Turnstile is not configured, `turnstile_token` can be omitted.
- On success, use returned `token` exactly like `sso_token` in hosted flow.

## Turnstile Configuration

SSO operators can enable Turnstile per site in settings:

```yaml
settings:
  web:
    sites:
      sso:
        turnstile_site_key: YOUR_CLOUDFLARE_TURNSTILE_SITE_KEY
        turnstile_secret_key: YOUR_CLOUDFLARE_TURNSTILE_SECRET_KEY
```

Behavior:

- `turnstile_site_key` is exposed to frontend for widget rendering.
- `turnstile_secret_key` stays server-side and is used to verify token with Cloudflare.

## Security Checklist

1. Always use HTTPS for both SSO endpoint and callback endpoint.
2. Always validate `state` to prevent CSRF/login mixup.
3. Never keep `sso_token` in browser URL after processing.
4. Never log full token values in plaintext.
5. Prefer server-side token handling and secure HttpOnly session cookies.
6. Validate issuer and expiry even if you use remote introspection.
7. Reject reused, malformed, or empty tokens.

## Troubleshooting

1. "Unsupported redirect host"

- Your `redirect_to` host is not in allowlist (`*.laisky.com` or internal IP ranges).

2. Login keeps failing with generic message

- Credentials may be wrong.
- Turnstile may be enabled but missing/invalid.

3. `WhoAmI` returns unauthorized

- Token is missing/expired/invalid.
- Authorization header format must be `Bearer <token>`.

4. Token appears in logs or analytics

- Sanitize query string logging.
- Clean URL immediately after callback processing.

## Appendix: API Examples

### A. Redirect user to SSO

```http
GET https://sso.laisky.com/sso/login?redirect_to=https%3A%2F%2Fapp.laisky.com%2Fauth%2Fsso%2Fcallback%3Fstate%3Dabc123
```

### B. Validate token with `WhoAmI`

```bash
curl -X POST 'https://sso.laisky.com/query' \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer <sso_token>' \
  -d '{"query":"query { WhoAmI { id username } }"}'
```

### C. Custom UI login via GraphQL

```bash
curl -X POST 'https://sso.laisky.com/query' \
  -H 'Content-Type: application/json' \
  -d '{
    "query":"mutation SsoLogin($account:String!, $password:String!, $turnstileToken:String){ UserLogin(account:$account, password:$password, turnstile_token:$turnstileToken){ token user { id username } } }",
    "variables":{
      "account":"alice@example.com",
      "password":"***",
      "turnstileToken":"<cloudflare-turnstile-token>"
    }
  }'
```
