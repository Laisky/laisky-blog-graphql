# SSO Identity and Persistence Requirements

## Public Identity

- Every SSO user has one stable UUID used by GraphQL `BlogUser.id`,
  `UserProfile.uid`, JWT `sub`, and JWT `uid`.
- JWT `sub` and `uid` must be equal canonical UUIDs.
- Switching persistence backends must not change an issued SSO UUID or the
  ObjectID stored in existing blog authorship fields.

## Authoritative Store

- Exactly one user store is authoritative at runtime. When `oneapi` is
  selected, every password, email-code, OIDC, TOTP, passkey, profile, and
  authenticated-user operation uses the OneAPI SQL repository.
- Failure to connect to or validate the selected OneAPI database fails startup.
  There is no runtime fallback to MongoDB.
- OneAPI owns migrations for `users`, `tokens`, `options`, and
  `passkey_credentials`. SSO may validate but never migrate those tables.
- SSO owns its identity-link, OIDC-ownership, email-code, and pending-TOTP
  tables in the same SQL database.

## MongoDB Store Compatibility

- While `user_store` is `mongo`, every existing blog account keeps working with
  the credentials it already has: accounts without a `status` field are active,
  passwords and accounts are verified with the bounds they were created under,
  and passkeys registered with the legacy ObjectID user handle still sign in.
- Explicitly inactive accounts (for example `pending`) are rejected by every
  login method and by token validation, before any token is issued.

## Compatibility and Security

- Password creation and verification are byte-compatible with OneAPI bcrypt.
- Disabled and deleted OneAPI users cannot establish or continue an SSO
  session, including through a previously issued JWT or passkey.
- Email challenges are hashed, expiring, and single-use under concurrency.
- Passkey login ceremonies are single use; a replayed session or a
  non-advancing non-zero signature counter is rejected.
- Only credential rejections count toward the Turnstile failure threshold;
  infrastructure failures are reported as `login_unavailable`, and a rejected
  Turnstile token as `turnstile_failed`, never as invalid credentials.
- Before redirecting to an external client, the SSO page persists the issued
  session on the SSO origin so return visits reuse it.
- Provider subjects and passkey credential IDs have one owner; all mutations
  enforce that owner.
- Pending TOTP secrets are not written to OneAPI's confirmed
  `users.totp_secret` field before successful verification.
- Logs never contain passwords, TOTP secrets, email codes, passkey material,
  database DSNs, tokens, or authorization headers.
