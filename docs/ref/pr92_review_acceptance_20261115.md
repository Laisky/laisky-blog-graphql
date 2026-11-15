# PR 92 maintainer review follow-up (2026-11-15)

Scope: the maintainer review of PR #92 (score 7.6/10). It covered two P2 findings, a deployment-dependent P2, three P3 findings and maintainability notes. Each defect was first reproduced with a behavior test that failed on the previous head (`9d3eb70`, which is `fbdc585` with master `b5996cc` merged), then fixed, and the same test was re-run green. No production systems were touched and no stored data was migrated in place.

## Findings, reproduction and fix

| Review item | Reproduction (red on previous head) | Fix |
| --- | --- | --- |
| P2: registration codes not counted per account | `TestMongoRegistrationCodeAccountBudgetSurvivesIPRotation`: a 4th wrong code from a fresh address was not challenged. `TestMongoConcurrentRegistrationGuessesRespectAccountBudget`: 50 of 50 concurrent guesses reached the code check (the bound is 3). | `UserRegister` and `UserRequestEmailCode(register)` use the account-scoped `registerFlow(account)`. Only `model.ErrInvalidEmailVerificationCode` counts as a failure; storage and policy errors release without counting. |
| Root cause behind it: a wrong guess left the code valid for 30 minutes | `TestOneAPIEmailCodeAttemptsAreCapped` and `TestMongoEmailCodeAttemptsAreCapped`: the right code was still accepted after 5 wrong ones, sequentially and in a 40-way burst, for both purposes and both stores. `TestOneAPIRegistrationCodeErrorsAreClassified`: OneAPI registration returned unclassified errors. | A per-code attempt cap of 5 (`EmailVerificationCodeMaxAttempts`) is spent in the store before each comparison: MongoDB `findOneAndUpdate` with `$inc` and `$not/$gte`, SQL a conditional `UPDATE` with `COALESCE`. One verification path now serves both stores and both purposes. |
| P2: loose fold conflates distinct MongoDB accounts | `TestMongoCollidingAccountSignInKeepsVictimChallenged` (real `alice@` and `alicé@` accounts): after the colliding owner signed in, the next guess against the victim was not challenged. Four tracker tests failed, including 3 free concurrent guesses. | Store identity is authoritative. A spelling links up to 4 identities, and admission checks and reserves all of them. A success clears only its own identity and spellings linking nothing else. |
| P3: success scan holds the global mutex | Benchmark on a full 10k table: `failure_then_success` took 232,555 ns/op and `parallel_mixed` 69,005 ns/op. | A bounded advisory identity→spelling index replaces the scan. It is re-checked before use, with the invariants documented. Results: 797 ns/op and 6,622 ns/op. |
| P3: Turnstile documentation contradicted validation | Documentation only | The SSO manual states that both keys or neither must be set. |
| P2: broad default trusted networks | Deployment-dependent | Startup now warns when `trusted_proxies` is unset (`Policy.StartupWarning`). The client-identity manual separates application acceptance from ingress acceptance and gives operator verification steps. |
| P3: process-local tracker | Documentation only | The SSO manual and requirements state that budgets are per process. The per-code cap is stored with the code and applies across replicas. |
| Duplicated hostname normalization | Two identical copies | Replaced by `clientip.NormalizeHost`, `NormalizeHosts` and `RequestHostname`. Both callers' existing tests are retained. |

The controls passed before and after the fix: `TestMongoRegistrationSucceedsAfterTyposAndResetsTheBudget`, `TestMongoLegacyEmailCodeStillVerifies` and `TestAuthSuccessStillResetsAnUncontestedSpelling`.

## Independent review of the follow-up

An adversarial read-only review of the follow-up found three more defects. Each was reproduced with a failing test, then fixed. The reviewer's own reproduction files, run through `go test -overlay`, also pass after the fix.

| Finding | Reproduction | Fix |
| --- | --- | --- |
| Identity-less failures on a shared spelling could be cleared by a colliding sign-in. Wrong email codes are rejected before the account is loaded. The same happened when the 4-identity bound overflowed. | `TestAuthSuccessKeepsUnattributedFailuresOfCollidingSpelling`, `TestAuthLinkOverflowKeepsSpellingEvidence`, and the end-to-end `TestMongoCollidingAccountEmailCodeGuessesStayChallenged` | A spelling records the exact (trimmed, lower-cased) spelling of each identity-less failure. Overflow marks the spelling contested. A success clears a spelling only when all of its evidence is provably its own (`forgivableBy`). Control: `TestAuthSuccessClearsOwnUnattributedTypos`. |
| The startup warning was silent with `client_ip_headers: []`, yet trusted peers still set the forwarded host and proto. | `TestPolicyStartupWarning` | The headers exemption is removed. |
| MongoDB could hold several live codes and verified an arbitrary one. | `TestMongoOnlyTheNewestEmailCodeIsVerified` | One `findOneAndUpdate` sorted newest-first spends the attempt and returns the count after the update. Superseded codes are never compared. |
| Registration storage errors now reached the client verbatim. | `TestOneAPIRegistrationCodeErrorsAreClassified`, `TestMongoRegistrationInfrastructureFailureIsNotACodeFailure` | `model.NewLoginUnavailable` wraps the cause for the log. Clients see only `login_unavailable`. |

The full suite also caught a mocked wire fixture (`TestPasswordCompatibilityWriters`) that scripted the old `find` command. The fixture now scripts `findAndModify` and asserts its `$inc`, sort and `new` options.

## Benchmark (AMD Ryzen 7 5700G, `-count=3`, final code)

| Case | Before (full scan) | After (index) |
| --- | --- | --- |
| `failure_then_success`, 10k-record table | 232,555 ns/op | 790–914 ns/op |
| `parallel_mixed` | 69,005 ns/op | 7,708–8,450 ns/op |
| `failure_new_account` (insert + eviction) | 7,753 ns/op | 12,080–16,134 ns/op (adds the exact-spelling HMAC per request) |

## Backward compatibility

- Existing MongoDB codes have no `attempts` field. They match `$not: {$gte: 5}` and start at zero; covered by `TestMongoLegacyEmailCodeStillVerifies`.
- On existing OneAPI tables, `Prepare`/AutoMigrate adds `attempts NOT NULL DEFAULT 0`. Existing rows read 0 and remain usable; the test passed on SQLite and PostgreSQL 17 (`TestRepoPrepareKeepsExistingEmailCodesUsable`). MySQL was not run; GORM issues the same `ADD COLUMN ... NOT NULL DEFAULT 0`.
- A user who mistypes a code 5 times must request a new one. The frontend now says so (`describeSsoAuthError`).
- The registration error text is now exactly `invalid email verification code`, without the old `register user: verify email code:` prefix. Infrastructure and policy errors keep their previous wrapping.
- The 30-minute code lifetime is unchanged (see `docs/ref/email_code_attempts_20261115.md`).

## Verified checks (final working tree)

- **`make test`** (`go test -race -cover -timeout 5m ./...`): all 58 packages passed. It ran with `MONGO_QUERY_TEST_URI` pointed at a disposable `mongo:8.0` and `ONEAPI_EMAIL_CODE_TEST_POSTGRES_DSN` at a disposable `postgres:17`. Both containers were removed afterwards. `go.mod` and `go.sum` are unchanged.
- **`make lint`** passed:
  - gofmt and goimports
  - module tidiness and vet
  - golangci-lint: 0 issues
  - govulncheck: 0 reachable vulnerabilities, and 2 module-only findings that the code does not call
  - the pure-Go and `system_owner` guards
- **Named fast CI** (`.scripts/fast_ci.py`) passed: 14 suites, including the new service and oneapi SQLite selections. Its Python validator passed 10 tests.
- **Frontend**, with Node 22.22.3, pnpm 10.34.6 and a frozen lockfile: `pnpm run lint` is clean, `pnpm run test` passed 59 files and 1,218 tests, and `pnpm run build` passed.
- **Not run:** MySQL. The `ADD COLUMN ... NOT NULL DEFAULT 0` migration was exercised on SQLite and PostgreSQL only. Hosted CI runs separately after push.
