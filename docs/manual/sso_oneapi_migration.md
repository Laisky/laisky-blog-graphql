# OneAPI SSO User Migration Runbook

This runbook moves SSO identity persistence from the legacy MongoDB `users`
collection to OneAPI's SQL user database without changing public SSO UUIDs or
MongoDB blog-author ObjectIDs.

## Preconditions

1. Back up the OneAPI SQL database and the MongoDB `users` collection.
2. Upgrade OneAPI first. Its migrations must have populated unique UUIDs for
   users, tokens, and passkeys.
3. Configure the same WebAuthn relying-party ID and compatible origins in both
   services before moving passkeys.
4. Keep `settings.web.sso.user_store: mongo` during preflight and import.

## Preflight

Run the idempotent preflight with the normal service configuration:

```bash
go run main.go -c /path/to/settings.yml migrate sso-oneapi
```

Compare normalized email addresses across both stores and stop on:

- duplicate emails or usernames;
- duplicate GitHub subjects;
- duplicate passkey credential IDs;
- invalid or duplicate Mongo SSO UIDs;
- one Mongo user matching multiple OneAPI users; or
- one OneAPI user matching multiple Mongo users.

Do not copy Mongo password hashes into OneAPI. They use a different format.
Mongo-only users need a password reset or email-code login followed by a new
password.

## Import Rules

After resolving every preflight conflict, apply the import:

```bash
go run main.go -c /path/to/settings.yml migrate sso-oneapi --apply
```

For each unambiguous email match:

1. Insert one idempotent `sso_user_links` row containing the OneAPI user ID and
   UUID, the existing Mongo SSO UID, and the existing Mongo ObjectID.
2. Import GitHub identity ownership into `sso_oidc_identities` and mirror the
   subject to `users.github_id` only when no conflicting binding exists.
3. Import confirmed TOTP secrets only. Discard pending enrollments.
4. Convert embedded Mongo passkeys into OneAPI's native
   `passkey_credentials` representation. Preserve credential IDs, public keys,
   signature counters, flags, AAGUIDs, and transports.
5. Do not migrate active email codes or signed ceremony sessions.

Repeat the import to prove idempotency, then compare user, identity, TOTP, and
passkey counts. Resolve every conflict explicitly.

## Cutover

1. Pause SSO profile mutations briefly.
2. Run the final import and invariant audit.
3. Set `settings.web.sso.user_store: oneapi` and deploy.
4. Verify password, email-code, GitHub, TOTP, and passkey login; `WhoAmI`; JWT
   `sub == uid`; profile state; and creation/update of a blog post.
5. Confirm historical posts still resolve the same author and retain ownership.

The OneAPI path fails closed. Never configure an automatic fallback to Mongo,
because that would create split-brain passwords and identity bindings.

## Rollback

Before OneAPI receives SSO writes, reverting the selector is sufficient. After
cutover writes occur, freeze SSO mutations and reconcile new users, password
changes, TOTP state, passkeys, GitHub bindings, and identity links back to the
chosen recovery store before changing the selector. Do not deploy the old
binary against diverged data.
