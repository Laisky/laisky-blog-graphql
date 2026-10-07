# RCA: Owner Cannot Sign In Through SSO or Edit the CV

**Reported:** 2026-10-07
**Issues:** [laisky-blog-graphql#52](https://github.com/Laisky/laisky-blog-graphql/issues/52), [go-ramjet#75](https://github.com/Laisky/go-ramjet/issues/75)
**Affected:** `sso.laisky.com` (MongoDB user store), `cv.laisky.com` editing

## Menu

- [Symptoms](#symptoms)
- [Production Facts](#production-facts)
- [Root Causes](#root-causes)
- [Fixes](#fixes)
- [Verification](#verification)
- [Remaining Operator Steps](#remaining-operator-steps)

## Symptoms

Password and passkey sign-in failed at `https://sso.laisky.com/?redirect_to=https://cv.laisky.com/`,
Turnstile kept reappearing, and the CV editor reported an "expired" SSO token.

## Production Facts

Collected read-only on 2026-10-07; no login attempts or data changes were made.

- The running image was built from `master` at `6efdff9`. Its configuration
  matches `/opt/configs` and leaves `settings.web.sso.user_store` unset, so the
  MongoDB store is authoritative. The derived SSO signing key fingerprint
  matched the published public key.
- The `users` collection holds three accounts. Two, including the owner, have
  **no `status` field**. The owner has a UID, no TOTP and one passkey created at
  `2026-07-02T15:09:47Z`.
- That passkey predates commit `1f285e6` (`2026-07-02T15:56:08Z`), which changed
  the WebAuthn user handle from the hex ObjectID to the UID.
- nginx logged the owner's attempts at 15:15–15:18 UTC (passkey start/finish and
  several password submissions). The CV logged `GET /cv/content/history 401`
  with a stored SSO token at 15:15:34 UTC.

## Root Causes

1. **Legacy accounts treated as inactive.** `30ee1b2` (OneAPI integration)
   required `status == "active"` in token validation, email-code login and
   GitHub login. Accounts without a status field (the owner) were rejected by
   `WhoAmI` and `UserProfile` even after a token was issued, so every method
   ended in a failure, and the login page discarded the stored session.
2. **Legacy passkey handle rejected.** The adapter returned the UID while the
   owner's authenticator still presented the ObjectID handle; go-webauthn
   rejects the mismatch ("User handle and User ID do not match").
3. **Legacy password bound.** `30ee1b2` cut the verification bound for existing
   MongoDB passwords from 1024 to 20 characters, before the store was chosen.
   Each rejection counted as a credential failure and raised Turnstile.
4. **Risk policy amplification.** Database or signing failures were counted as
   bad credentials; a rejected Turnstile token was reported as "invalid
   credentials"; the TOTP step demanded a second challenge.
5. **Session not kept on the SSO origin.** Password, email-code, passkey and
   GitHub flows redirected to the client before storing the session, so return
   visits asked for credentials again.
6. **CV verifier mismatch.** CV's protected routes used the shared HS256
   middleware keyed by `server.jwt_secret`, which cannot verify EdDSA SSO
   tokens, and had no owner authorization at all.

Found while reproducing, fixed in the same change:

- A finished passkey session could be replayed within its 10-minute lifetime;
  zero-counter (synced) passkeys had no other protection, and a non-advancing
  counter was ignored.
- MongoDB email codes were not single use under concurrency.
- GitHub display names longer than 20 characters failed GitHub sign-in.

## Fixes

- `model.User.IsActive()` is the one account-state rule: `active`, or no status
  for MongoDB accounts; explicit inactive states are rejected everywhere,
  including password and passkey login before a token is issued.
- Existing-credential bounds are store specific (MongoDB 128/1024, OneAPI
  50/20); new-password policy is unchanged.
- Passkey login accepts the credential owner's UID or legacy ObjectID handle
  only, consumes each signed session once, and rejects non-advancing counters.
- Login errors are classified: `invalid credentials` (counted),
  `login_unavailable` (not counted), `turnstile_failed`. A solved challenge
  earns a bounded allowance that a credential failure revokes.
- The SSO page stores the session before every external redirect.
- CV verifies sessions with SSO `WhoAmI` against a fixed endpoint, cross-checks
  the token subject, authorizes only `tasks.cv.sso.owner_uids`, and reports
  401/403/503 distinctly; the editor enables itself only after
  `GET /cv/auth/session` succeeds.

## Verification

Behavior tests were written first and recorded failing on the unmodified code,
then turned green; each fix was also reverted individually (mutation check) to
confirm its test turns red again.

- `internal/web/sso_mongo_login_e2e_test.go`: GraphQL e2e on real MongoDB —
  legacy password login reaching `WhoAmI`/`UserProfile`, UID backfill, wrong
  password, pending account, TOTP, and a passkey matrix (legacy and UID
  handles; foreign handle, forged signature, foreign origin, foreign challenge,
  replay, counter regression, zero-counter replay, pending owner).
- `internal/web/blog/controller/login_risk_mongo_test.go`: outage vs credential
  failures, TOTP after a solved challenge, rejected Turnstile token.
- `internal/web/blog/service/users_legacy_mongo_test.go`: email code, GitHub
  binding, password change, TOTP disable for legacy accounts; concurrent
  email-code use.
- `web/src/pages/sso-login-handoff.test.tsx`: session persisted before every
  external redirect, return-visit reuse, TOTP challenge, error wording.
- go-ramjet `internal/tasks/cv/sso_owner_auth_test.go` and
  `web/src/pages/cv/cv-auth.test.tsx`: owner/non-owner/invalid/unavailable
  sessions at the real route boundary, no side effects on rejection, no token
  forwarding on redirect, URL scrubbing.

## Remaining Operator Steps

1. Deploy both images.
2. Add the owner UID to go-ramjet `tasks.cv.sso.owner_uids`; without it CV
   editing fails closed with `cv_owner_not_configured`.
3. Sign in as the owner with password and passkey, then edit and save the CV.
