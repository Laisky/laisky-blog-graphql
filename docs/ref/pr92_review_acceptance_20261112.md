# PR 92 review revision (2026-11-12)

Scope: retire the legacy BlogLogin mutation as explicitly requested, fix known-alias reset, reject incomplete Turnstile pairs, and count successful authentication only after token issuance. No production changes or stored-data migration.

## Reproduction and controls

- On the original branch implementation, both `TestAuthSuccessResetsAllKnownAliases` and `TestAuthSuccessPreservesAliasReservations` failed. The first also checks an unrelated attacked account remains challenged; the second preserves concurrent reservations and counts a later failure.
- `TestMongoSigningFailureDoesNotResetRisk` failed on the original implementation: signing failed but the previous failure count was zero rather than one. It passes only when issuance precedes success recording; a subsequent valid issuance resets the count.
- Twenty incomplete/invalid Turnstile-pair subcases failed before validation; complete and unset pairs remain accepted. Startup tests cover global and selected/non-default site pairs, regardless of fallback configuration.
- Schema regression rejects BlogLogin before resolver execution and validates modern UserLogin. The response type remains for modern callers. No frontend BlogLogin callers were found.
- The secret-only runtime gate returns sanitized `login_unavailable`, rather than granting an exception or an unsolvable challenge.

## Design and compatibility

A bounded scan of the existing account table clears every known alias for an authenticated identity. Keeping the existing mutex and table avoids a second alias index with separate eviction and concurrency invariants. In-flight alias reservations are not deleted. Both password and email-code login record success after response generation. All supported authentication entry points retain normal challenge enforcement; legacy bypass logic and warning machinery are removed.

The legacy mutation retirement is the explicit intentional compatibility break. Existing users, stored records, JWT format, and modern response shape are unchanged. Operators must configure both public and secret Turnstile keys or neither; pairless alias sites can still use a complete fallback pair.

## Evidence availability

Earlier task-history/FileIO access was unavailable/denied in this session. No claim is made that an unavailable history search preceded implementation, or that FileIO closeout has been recorded and read back. This repository evidence and the PR description preserve the work until canonical recording is available. No credentials or request payloads are included.

## Verified checks

- `go test -race -count=3 ./internal/web/blog/controller ./internal/web ./cmd` passed against isolated `mongo:8.0` (controller 289.821s, web 249.706s, cmd 4.313s).
- `make lint` passed: formatting, module tidiness, vet, golangci-lint (0 issues), govulncheck (0 reachable vulnerabilities), pure-Go and system-owner guards. Govulncheck reported two module-only findings outside imported/reachable code.
- Named fail-closed fast CI passed, including the new regressions; its Python validator suite passed 10 tests.
- `make gen` and `git diff --check` passed.
- Independent read-only review found no new actionable P2+; obsolete schema deprecation comment removed.

- Full `make test` passed with `MONGO_QUERY_TEST_URI` pointed at the disposable fixture (`-race -cover -timeout 5m ./...`).

## Latest-base integration

Integrated master `9c41454` after the review-fix commit. Conflicts were resolved by keeping both trusted-client/auth-risk and SSO redirect controls: both startup validators and their tests, the union of named fast-CI suites, and both documentation sections. Independent review verified the automatically merged GitHub, passkey and server paths retain trusted-forwarding enforcement and SSO completion revalidation.

The full Mongo suite exposed a stale synthetic passkey issuer: its request used the implicit `http://example.com`, while its WebAuthn config used `https://sso.example.test`. The merged redirect classifier intentionally rejects arbitrary external issuers even for a self target. Changing only the request to HTTPS was insufficient. The harness now consistently uses supported `https://sso.laisky.com` and its matching RP ID, entirely in-process with no network call. Production policy was not relaxed. All existing foreign-origin, wrong-handle, forged-signature, replay and counter controls remain; `TestMongoSSOPasskeyLoginHandles` passes with `-race -count=3`.

Merged `make lint` and the combined named fast gate pass. Frontend verification used Node 22.22.3 and the CI-required pnpm 10 (10.34.6), frozen lockfile: 58 files / 1214 tests passed and production build passed. The initially available pnpm 11 rejected the lockfile configuration; no lockfile changes or bypass flags were used.

Final merged full `make test` passed with the real isolated Mongo fixture after the issuer-fixture correction.
