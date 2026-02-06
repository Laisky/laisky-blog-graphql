# SSO Integration Manual

This manual is for engineering teams integrating Laisky SSO into their own systems.
It focuses on integration contracts and implementation details needed by consumers.

## Menu

- [SSO Integration Manual](#sso-integration-manual)
  - [Menu](#menu)
  - [Who Should Read This](#who-should-read-this)
  - [Integration Overview](#integration-overview)
  - [Quick Start (Recommended Flow)](#quick-start-recommended-flow)
  - [Protocol Contract](#protocol-contract)
    - [1. Login Entry URL](#1-login-entry-url)
    - [2. `redirect_to` Validation Rules](#2-redirect_to-validation-rules)
    - [3. Callback Contract](#3-callback-contract)
  - [JWT Token Specification](#jwt-token-specification)
    - [Token Format](#token-format)
    - [Claim Definitions](#claim-definitions)
    - [Decoded Token Example](#decoded-token-example)
  - [Token Validation](#token-validation)
    - [Option A (Recommended): Validate via GraphQL `WhoAmI`](#option-a-recommended-validate-via-graphql-whoami)
    - [Option B (Optional): Local JWT Verification](#option-b-optional-local-jwt-verification)
  - [Reference Integration Implementation](#reference-integration-implementation)
  - [Common Failure Cases](#common-failure-cases)
  - [Security Checklist](#security-checklist)
  - [Go-Live Checklist](#go-live-checklist)

## Who Should Read This

- Teams integrating login with Laisky SSO.
- Backend engineers implementing callback and session exchange.
- Frontend engineers implementing "Login with SSO" redirect.

This manual is intentionally consumer-focused and does not cover SSO operator/admin configuration.

## Integration Overview

Laisky SSO provides a hosted login page. After user authentication, SSO redirects back to your callback URL with `sso_token` appended in the query string.

- GraphQL endpoint (for token introspection): `/query`
- Callback token parameter: `sso_token`

## Quick Start (Recommended Flow)

1. Your app redirects the user to:

```text
https://sso.laisky.com/?redirect_to={URL_ENCODED_CALLBACK_URL}
```

2. User completes login on the SSO page.
3. SSO redirects the browser to your callback URL with `sso_token=<JWT>`.
4. Your backend validates `sso_token` (recommended: call `WhoAmI`).
5. Your backend creates your own session and redirects to a clean URL (without `sso_token`).

## Protocol Contract

### 1. Login Entry URL

Use this pattern:

```text
https://sso.laisky.com/?redirect_to=https%3A%2F%2Fapp.laisky.com%2Fauth%2Fsso%2Fcallback%3Fstate%3Dabc123
```

`redirect_to` should be your backend callback endpoint, with your own anti-CSRF `state` included.

### 2. `redirect_to` Validation Rules

The SSO login page accepts only:

- Protocol: `http`, `https`
- Host:
  - `laisky.com` and subdomains (`*.laisky.com`)
  - Internal IP ranges:
    - IPv4: `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`, `127.0.0.0/8`, `100.64.0.0/10`
    - IPv6: `::1`, `fc00::/7`

If your callback host is outside this policy, contact the SSO team before release.

### 3. Callback Contract

After successful login, browser is redirected to your `redirect_to` URL with query parameter `sso_token=<JWT>` appended.

Behavior details:

- Existing query parameters in `redirect_to` are preserved (for example `state`).
- `sso_token` is added; if `sso_token` already exists, it is overwritten by the new token.

Your callback endpoint should:

1. Validate `state`.
2. Read `sso_token`.
3. Validate `sso_token`.
4. Create your own session (recommended: secure HttpOnly cookie).
5. Redirect to a clean URL and remove `sso_token` from browser address.

## JWT Token Specification

### Token Format

`sso_token` is a standard JWT:

```text
base64url(header).base64url(payload).base64url(signature)
```

- Signing algorithm: `HS256`
- Current token TTL: about 90 days (`3 * 30 * 24h`)

### Claim Definitions

| Claim          | Type   | Required | Description                                      |
| -------------- | ------ | -------- | ------------------------------------------------ |
| `iss`          | string | yes      | Token issuer. Current value: `laisky-sso`.       |
| `sub`          | string | yes      | User ID in hex string format (MongoDB ObjectID). |
| `iat`          | number | yes      | Issued-at time (Unix seconds, UTC).              |
| `exp`          | number | yes      | Expiration time (Unix seconds, UTC).             |
| `jti`          | string | yes      | Token ID (UUIDv7 string).                        |
| `username`     | string | yes      | User account/login identifier.                   |
| `display_name` | string | yes      | User display name.                               |

### Decoded Token Example

Header:

```json
{
  "alg": "HS256",
  "typ": "JWT"
}
```

Payload:

```json
{
  "iss": "laisky-sso",
  "sub": "66c9f85d31dc8f4eb9a4df0a",
  "iat": 1738929600,
  "exp": 1746705600,
  "jti": "0194d5f8-19f7-7f7b-a8d3-421a60f8d8ab",
  "username": "alice@example.com",
  "display_name": "Alice"
}
```

## Token Validation

### Option A (Recommended): Validate via GraphQL `WhoAmI`

Send token as Bearer token to SSO GraphQL endpoint:

- Endpoint: `https://sso.laisky.com/query`
- Header: `Authorization: Bearer <sso_token>`

Query:

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

If successful, treat token as valid and map the returned user into your local account/session model.

### Option B (Optional): Local JWT Verification

Use this only when your team is explicitly allowed to use the shared SSO signing secret.

Minimum validation rules:

1. Verify JWT signature with `HS256`.
2. Verify `exp` and `iat`.
3. Verify `iss == laisky-sso`.
4. Verify `sub` is a valid expected user ID format.

## Reference Integration Implementation

```ts
// 1) Build login URL
const state = randomString();
const callback = `https://app.laisky.com/auth/sso/callback?state=${encodeURIComponent(state)}`;
const loginURL = `https://sso.laisky.com/?redirect_to=${encodeURIComponent(callback)}`;

// 2) Redirect user to loginURL
window.location.assign(loginURL);

// 3) Callback handler (backend)
// - validate state
// - read sso_token
// - call SSO /query WhoAmI with Authorization: Bearer <sso_token>
// - create local session
// - redirect to clean page (remove sso_token from URL)
```

## Common Failure Cases

1. `Missing redirect_to parameter`: your login URL did not include `redirect_to`.
2. `Unsupported redirect protocol`: your callback URL is not `http` or `https`.
3. `Unsupported redirect host`: your callback host is not under `*.laisky.com` and not an allowed internal IP.
4. `Unauthorized` from `WhoAmI`: token is missing, malformed, expired, or invalid, or Authorization header is not `Bearer <token>`.
5. Callback loops or fails after success: `state` mismatch, or callback handler did not persist local session before redirect.

## Security Checklist

1. Always use HTTPS in production.
2. Always validate `state` to prevent CSRF/login mix-up attacks.
3. Treat `sso_token` as a credential; never log full token values.
4. Process token server-side whenever possible.
5. Remove `sso_token` from URL immediately after callback processing.
6. Use short-lived local session cookies with `HttpOnly`, `Secure`, and `SameSite`.

## Go-Live Checklist

1. Callback URL is finalized and tested end-to-end.
2. Callback URL passes `redirect_to` validation policy.
3. Backend callback validates token using `WhoAmI` or approved local verification.
4. `state` anti-CSRF logic is implemented and tested.
5. URL cleanup after callback is implemented.
6. Monitoring and logs do not expose full JWT values.
