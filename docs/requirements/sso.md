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

## Compatibility and Security

- Password creation and verification are byte-compatible with OneAPI bcrypt.
- Disabled and deleted OneAPI users cannot establish or continue an SSO
  session, including through a previously issued JWT or passkey.
- Email challenges are hashed, expiring, and single-use under concurrency.
- Provider subjects and passkey credential IDs have one owner; all mutations
  enforce that owner.
- Pending TOTP secrets are not written to OneAPI's confirmed
  `users.totp_secret` field before successful verification.
- Logs never contain passwords, TOTP secrets, email codes, passkey material,
  database DSNs, tokens, or authorization headers.
