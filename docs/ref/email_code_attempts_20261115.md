# Email code attempt limits and account identity (2026-11-15)

Research behind the PR #92 follow-up that caps verification attempts per
emailed code and makes registration codes count against the account budget.
Sources retrieved 2026-11-15.

## Standards

- **NIST SP 800-63B-4** (<https://pages.nist.gov/800-63-4/sp800-63b.html>, page dated 2025-08-26)
  - §3.2.2: failed attempts are limited per authenticator per subscriber
    account, at most 100. CAPTCHA-style bot checks and growing delays MAY reduce
    lockout risk. Prior failures SHOULD be disregarded after a successful
    authentication.
  - §3.1.3.1: email SHALL NOT be used for out-of-band authentication.
    Confirmation codes that validate an email address are exempt ("not
    authentication processes"). Registration codes are therefore confirmation
    codes. The passwordless email-code login is outside 800-63B by design.
  - §3.1.3.2: out-of-band secrets are valid for 10 minutes and accepted only
    once. Below 64 bits, rate limiting is required, and generating a new secret
    SHALL NOT reset the failed-attempt count.
  - §4.2.1.2: emailed confirmation and recovery codes are valid for at most 24
    hours and are subject to the same throttling.
- **OWASP ASVS 5.0.0** (2025-05-30, `5.0/en/0x15-V6-Authentication.md`)
  - 6.5.1: single use.
  - 6.5.4: at least 20 bits of entropy (6 digits).
  - 6.5.5: out-of-band codes live at most 10 minutes.
  - 6.6.3: code-based out-of-band verification must rate-limit brute force.
  - 6.3.6 and the V6.6 introduction rule out email as an authentication factor.
- **OWASP cheat sheets** (Multifactor Authentication, Authentication, Forgot
  Password): use short TTLs, single use and "strict attempt limits", and
  invalidate the code on success. Count failures per account, not per source
  IP. Lockout can be abused for denial of service. No numeric limits are given.

## Industry practice

- **Auth0 passwordless** (<https://auth0.com/docs/authenticate/passwordless/best-practices>):
  three failed attempts per code, a three-minute default lifetime, and only the
  newest code is accepted. Requesting a new code does not clear the attempt
  counter (support article, 2025-10-27).
- **Microsoft Entra B2B email OTP** (updated 2026-03-27): codes are valid for
  30 minutes. No attempt limit is documented.
- **Supabase**: one OTP request per 60 seconds and a one-hour default expiry. No
  attempt limit is documented.

## Concurrency

- **PortSwigger, race conditions**: bypassing anti-brute-force limits is a
  limit-overrun attack. The fix is atomic state changes in the datastore; one
  layer should not secure another, so an in-process counter cannot protect the
  store.
- **MongoDB** (manual 9.0, "write operations atomicity"): writes to a single
  document are atomic and re-check the filter. Use `findOneAndUpdate` with the
  attempt bound in the filter and `$inc`.
- **PostgreSQL 18 §13.2.1**: under READ COMMITTED, a concurrent `UPDATE` waits
  and re-evaluates its `WHERE` clause. `UPDATE ... SET attempts = attempts + 1
  WHERE id = ? AND attempts < ?` cannot exceed the bound.
- **MongoDB `$not`** (<https://www.mongodb.com/docs/manual/reference/operator/query/not/>):
  `{f: {$not: {$gt: N}}}` matches documents without `f`, whereas `$lte` does
  not. `{attempts: {$not: {$gte: N}}}` therefore keeps codes stored before the
  field existed.

## Decisions

- Each code allows 5 attempts. An attempt is spent before the comparison, in
  the store: MongoDB uses `findOneAndUpdate`, and SQL uses a conditional
  `UPDATE` with `COALESCE`. This is atomic across clients and replicas.
  Exhausting the attempts invalidates only that code, so there is no account
  lockout, in line with the project's challenge-never-lock rule.
- The rule that a new code must not reset the count is met by the
  adaptive-authentication account budget. Registration and code requests are
  now account-scoped. Failures accumulate per email across codes, and once the
  threshold is reached every further guess or new code needs a solved
  Turnstile challenge.
- The 30-minute code lifetime is unchanged, which is within the 24-hour bound
  for confirmation codes. Shortening it to the 10-minute out-of-band bound is
  left to the owner, because it changes user-visible behavior.
