# Telegram alert admission: rate-limit sources (2026-11-04)

Sources checked for the `TelegramMonitorAlert` throttle redesign (PR #81 review
follow-up). The design is in `library/throttle/throttle.go` and the manual is
`docs/manual/telegram_alert_delivery.md`.

## A missing bucket is a full bucket

GCRA and token-bucket limiters backed by Redis treat a missing key as a full
bucket. They let the key expire once the bucket would have refilled:

- go-redis/redis_rate v10 (`lua.go`): `if not tat then tat = now`, then
  `SET key new_tat EX ceil(new_tat - now)`.
  <https://github.com/go-redis/redis_rate/blob/v10/lua.go>
- redis-cell: a missing key is `now`. The TTL is `new_tat - now`, the time
  "until the limit will reset to its maximum capacity".
  <https://github.com/brandur/redis-cell>
- Stripe's limiter gist: a missing key starts at `capacity`. The TTL is
  `2 * capacity / rate`. <https://gist.github.com/ptarjan/e38f45f2dfe601419ca3af937fff574d>

It is safe to drop a bucket at or after its full-refill time. Dropping it
earlier would hand out sends the caller had not earned back. The throttle drops
a per-type budget only once it is computed to be full. Its sweep runs at most
once per full-refill time.

## New buckets start full

`golang.org/x/time/rate` v0.16.0 documents a bucket "initially full". `NewLimiter`
sets `tokens: float64(b)`. Starting full is also what makes "missing == full" hold.
<https://pkg.go.dev/golang.org/x/time/rate>

`x/time/rate` `Reservation.CancelAt` (v0.16.0 source, `rate.go:177`) returns
early when `r.timeToAct.Before(t)`. A reservation that could act immediately
restores nothing once time has moved on. It cannot express "return the send when
the credential lookup fails", so the throttle keeps its own bucket, which has an
explicit `Admission.Cancel`.

## Per-identity limits before authentication

The OWASP Authentication Cheat Sheet warns that account lockout can be used "to
cause a denial of service by locking out other users' accounts". ASVS 5.0 6.1.1
asks that rate limiting be configured to "prevent malicious account lockout".
<https://cheatsheetseries.owasp.org/cheatsheets/Authentication_Cheat_Sheet.html>

This is why only authenticated sends keep a per-type send spent. A reservation
made before the lookup is returned when the lookup fails. That bounds what an
unauthenticated caller can hold to the lookups it has in flight, and each of
those is paid for from the shared budget.

## Canonical keys

OWASP gives no normalization rule for rate-limit keys, but two CVEs show alias
bypasses:

- Authelia CVE-2026-47203: case variants of a username each got their own ban
  bucket. It was fixed by canonicalizing before regulation.
- Strapi CVE-2023-38507 (GHSA-24q2-59hm-rh9r): `/admin/Login` and trailing
  slashes bypassed the login limit.

The throttle keys every budget by `dao.NormalizeAlertName`, the same form the
credential lookup matches.

## Telegram delivery ceiling

The Bot FAQ (<https://core.telegram.org/bots/faq>) says to avoid more than one
message per second per chat, more than 20 per minute per group, and more than
about 30 per second broadcast. Paid broadcasts allow up to 1000 per second.
`total_*` should stay within the broadcast ceiling.
