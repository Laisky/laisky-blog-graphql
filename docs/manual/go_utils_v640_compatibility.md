# go-utils v6.4.0 consumer compatibility

This update stays on go-utils/v6. The release tag points to
6a02887d3dbf60ecadf21a67b5d3b9449fd942e6. The Go 1.27.0 module floor,
OneAPI bcrypt contract, indirect v3/v5 modules, local pgvector and avro
replacements, and retirement test dependencies remain in place.

## Mongo password records

The previous pin (506ec9758d5f) writes Argon2id PHC records even when the
caller supplies HashTypeSha256. v6.4.0 restores the legacy digest writer
behind PasswordHash and exposes PasswordHashArgon2id explicitly. A dependency
bump alone would therefore weaken newly registered and changed passwords.
The two Mongo writers and unknown-account timing mask explicitly retain
Argon2id. Login still verifies existing PHC and legacy SHA records without
rewriting them. OneAPI continues to use its separate bcrypt-compatible helpers.

The service regression suite exercises actual Mongo driver insert/update
commands and service/DAO login, including wrong passwords, malformed records,
unknown accounts and a distinct storage-error control. Immutable public
synthetic records cover the previous PHC writer, the updated consumer writer
and the historical salted 10,000-round SHA-256 layout. The updated-writer
fixture is also read by the unchanged baseline consumer during rollback
acceptance. These fixtures contain no operational account or password data.

## Scope and remaining acceptance

MCP file credentials directly use go-utils crypto/kms envelopes. Memory
adapters directly use the agents/files and agents/memory SDKs. Synthetic old
and updated consumer envelopes are read in both directions, with wrong AAD,
wrong key, malformed encoding and tamper rejection controls. Existing write/read,
EOF, directory, quota, tenant-isolation and memory-boundary tests, plus explicit
adapter empty-search/list and failure contracts, are consumer controls. SMTP
verification-code and OneAPI bcrypt tests cover their direct helper uses.
The updated SDK bounds directory-summary introspection to canonical descendants
of /memory/<session>, with depth/child-entry caps of 32/200, at most 64KiB per
abstract and 1MiB aggregate abstract bytes, and a ten-second deadline. Cross-session paths now fail; root and traversal paths remain rejected; the documented empty-path session
flow stays valid. Tests exercise both success and rejection through the real
SQLite-backed memory service. This tightening belongs to the library update;
callers using broad summary paths must use session-scoped paths instead.
No Sentry/frontend or MCP web_search diagnostic files are changed.

Validation receipts and exact commands are recorded in the pull request.
Environment-dependent skips are not database acceptance. Full lint, race,
coverage, build and external checks must be considered separately from focused
compatibility evidence.

## Rollout and rollback

This draft changes source and module dependencies only; it does not deploy.
Before an operator rollout, qualify the eventual combined source with concurrent
diagnostics/Sentry changes and the deployment environment. Existing stored
passwords are not migrated. Updated writers retain the PHC layout and parameters
already accepted by the previous pinned consumer. Rollback means deploying the
previous reviewed application artifact; it needs no password-data reversal.
Older digest-only historical releases are not supported rollback targets once
PHC records exist. No service configuration or security integration changes
are part of this dependency update.

## References

- [Pinned release password API](https://github.com/Laisky/go-utils/blob/v6.4.0/crypto/password.go)
- [Upstream password rollout policy](https://github.com/Laisky/go-utils/blob/v6.4.0/crypto/password_migration.md)
- [Consumer SSO requirements](../requirements/sso.md)
- [Retained local and manual acceptance](fast_ci.md)
