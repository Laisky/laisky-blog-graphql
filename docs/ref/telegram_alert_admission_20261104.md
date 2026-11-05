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

Both entrypoints share an in-memory credential directory, but MongoDB remains
authoritative. In the GraphQL path, a known pair reserves shared and per-type
delivery capacity before the lookup; a failed check refunds the per-type send
only to the extent safe after intervening refill and admissions. The shared send
remains spent. The webhook checks credentials before spending delivery capacity.

An unknown or unverifiable pair may be a remotely rotated credential. It uses a
separate bounded authoritative lookup: one per second with a burst of five and
at most five in flight. A failed lookup spends no delivery capacity; a successful
lookup proceeds to delivery admission. Exhaustion returns a retryable throttle
response, so a guessing flood can delay first-use or remotely rotated credentials,
while cached known pairs do not use that fallback budget. A pair the fallback
accepts is remembered. Directory refreshes are spaced 30 seconds apart for a
healthy copy, and one second apart after a failed read or a local credential
change (see below). See the [delivery manual](../manual/telegram_alert_delivery.md)
for the entrypoint-specific response behavior.

## Bounded waiting on directory reads (checked 2026-11-05)

Inspected version: `golang.org/x/sync` v0.24.0, which `go.mod` requires and
which `proxy.golang.org/golang.org/x/sync/@latest` reported as the latest
release on 2026-11-05. In its `singleflight.go`, `DoChan` makes a buffered
channel for every caller (L122) and appends it to the in-flight call's `chans`
(L129). The channels are released only when the call returns normally, through
the loop that sends each waiter its result (L176–177). L164–167 handle a
panicking call instead. A caller that stops waiting on its context still leaves
its channel behind, and `Forget` only deletes the map key (L210–212).
golang/go has no open proposal for removable or capped waiters. Tailscale's
fork adds `DoChanContext`, but it cancels only the shared work and still does
not limit waiters.
<https://github.com/golang/sync/blob/v0.24.0/singleflight/singleflight.go>

Overload guidance asks for small, bounded queues and early rejection:

- AWS Builders' Library: "When a server approaches overload, it should start
  rejecting excess requests." Also: "Early rejection is important because it's
  the cheapest place to drop excess traffic."
  <https://builder.aws.com/content/3Eun1EEyX6p2e3VYNyRLSJzLuMV/using-load-shedding-to-avoid-overload>
- Google SRE, *Addressing Cascading Failures*: it is "usually better to have
  small queue lengths relative to the thread pool size". Also: "fail early and
  cheaply".
  <https://sre.google/sre-book/addressing-cascading-failures/>
- RFC 5861 §3: a cache "SHOULD attempt to revalidate it while still serving
  stale responses (i.e., without blocking)". Caffeine's refresh returns the old
  value while one deduplicated refresh is in flight.
  <https://www.rfc-editor.org/rfc/rfc5861.html>
  <https://github.com/ben-manes/caffeine/wiki/Refresh>

The directory therefore runs one read at a time and signals its end by closing a
single channel. The Go memory model orders a close before any receive it
unblocks, so a woken waiter sees the new copy (<https://go.dev/ref/mem>). At most
`maxAlertCredentialReadWaiters` (32) misses wait, admitted by a non-blocking
buffered-channel semaphore. Every other miss answers at once from the copy as it
stands and takes the bounded fallback lookup.

This deliberately mixes pure stale-while-revalidate with waiting. A miss can be
a credential issued a moment ago, or the start of a cold-start burst. A small
number of waiters lets those reach storage through the directory instead of
spending the fallback budget. The limit is at least the shared delivery burst a
typical deployment configures, so waiting is not what limits a burst of genuine
deliveries.

## Refresh cadence, remembered credentials and read deadline (checked 2026-11-05)

A miss used to start a full read whenever the copy was a second old. Anyone can
send a miss, so an anonymous caller could keep MongoDB scanning up to 10,001
alert types every second. Self-service alert types also let that caller grow
the collection. Sources for the redesign:

- Kong polls for invalidations at a fixed `db_update_frequency` (default 5 s),
  not per request: "Frequency (in seconds) at which to check for updated entities
  with the datastore." `db_cache_neg_ttl` gives misses a TTL of their own.
  <https://developer.konghq.com/gateway/configuration/>
- AWS Builders' Library, *Reliability, constant work, and a good cup of coffee*:
  constant-work systems "don't scale up or slow down with load or stress". The
  read cadence is a fixed upper bound, independent of request rate.
  <https://builder.aws.com/content/3F05oqNtNUWxHJ5r6L6I2HrH4rI/reliability-constant-work-and-a-good-cup-of-coffee>
- AWS Builders' Library, *Caching challenges and strategies*: "cap the maximum
  rate of requests sent to the downstream service". Also: "consider maintaining
  a negative cache with a distinct TTL".
  <https://builder.aws.com/content/3K9TeXp6strMiJ4vhakpRD7BQWO/caching-challenges-and-strategies>

A healthy copy is therefore reread at most every 30 seconds, whatever the miss
rate. A failed read, or an alert type created or re-keyed in this process, is
reread after one second, the previous cadence. A storage refusal of a held pair
is reread at once, and that needs a pair that was once valid. A pair the bounded
fallback lookup accepts is remembered (positive caching), up to the directory's
limit. A credential another process creates therefore needs one fallback lookup,
not one per request until the next read. Only storage's acceptance adds a pair,
so guessing cannot grow the copy.

An incremental read, of alert types newer than the last `_id`, was considered and
not adopted. Rotations do not change `_id`. `modified_at` has no index. With
the read rate bounded, a full read at the 10,000 cap costs about 9 ms and 6 MB in
process every 30 seconds at most, which the read summary now reports.

The 5-second read deadline is enforced by the directory with `context.AfterFunc`
rather than trusted to the loader. The cursor close gets
`context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)`:

- Go: `WithoutCancel` "returns no Deadline or Err". `AfterFunc`'s stop
  "returns true if the call stopped f from being run".
  <https://pkg.go.dev/context>
- mongo-go-driver v1.17.10 `BatchCursor.Close` sends `killCursors` through
  `Operation.Execute(ctx)` when the cursor is not exhausted
  (`x/mongo/driver/batch_cursor.go`). Without a deadline, a stalled connection
  holds it until the client's connection timeouts. With this service's options,
  the Mongo contract test measured 31.0 s for a 1 s read.
- MongoDB closes an idle cursor that is never killed after `cursorTimeoutMillis`,
  10 minutes by default. A bounded best-effort kill is therefore safe.
  <https://www.mongodb.com/docs/manual/core/cursors/>

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
