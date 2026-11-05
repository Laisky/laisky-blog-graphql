# Complete Telegram monitor alerts

`TelegramMonitorAlert(type, token, msg)` now preserves the full chosen message.
It authenticates with the existing alert token, loads the existing subscriptions
and sends only to those recipients. The wire message remains
`type + " >>>>>>>>>>>>>>>>>> \n" + msg`. Literal Markdown/HTML, Unicode, whitespace
and URL strings are not escaped as a formatting dialect, redacted or cut.
An empty body remains valid and sends its existing type/header.

## Text, documents and admission

`settings.telegram.max_len` now selects the text-versus-document threshold; it
never truncates content. Its default remains 3000. Values from 1 through 4096
are accepted; invalid values use 3000. The count covers the complete formatted
message in conservative UTF-16 units. A smaller setting routes more alerts to
files instead of deleting their tails. Other bot messages keep their existing
send methods and formatting behavior.

Messages within that threshold use plain `sendMessage` JSON without a parse
mode, with link previews disabled. Larger messages use `sendDocument`, a fixed
`alert.txt` filename, no caption and the exact complete UTF-8 message bytes.
Document delivery adds no application file, persistent archive, endpoint or recipient.
Each recipient/call gets a fresh request/reader.

The complete formatted message is limited to **2 MiB**, including its type and
header. Oversized or invalid UTF-8 input is rejected intact before throttling,
authorization or sending. The service validates again before HTTP. This budget
fits the SDK's configurable 1 MiB complete alert plus the receiver header; the
SDK still defaults to 16 KiB and must be configured explicitly for larger inputs.
No record or field is replaced with an oversized marker. This limit applies
after GraphQL parsing, not before request-body allocation. Deployed proxy and
HTTP ingress budgets need separate release qualification; the source tests prove
loopback RPC handling, not production ingress or recipient delivery.

## Acceptance, failures and retries

The receiver makes one context-bound HTTP attempt per unique subscriber. Duplicate
subscription rows do not produce duplicate sends within a single mutation. It
preflights invalid/empty subscriber lists before attempting any send. Existing
fail-closed throttling and constant-time token validation remain authoritative.

Authentication and throttling run in this order:

1. **Canonical credentials.** The type and the token are put in the form
   `alert_types` stores and the token check compares: surrounding whitespace is
   trimmed, and an empty value, a null byte, or more than 100 runes (type) or 64
   runes (token) is refused with the DAO's `sanitize alert type: …` or
   `sanitize push token: …` error. Nothing is spent. Every later step uses that
   canonical name, so `" name "` and `"name"` share one budget.
2. **Credential directory.** Both entrypoints share a keyed-digest directory. In
   GraphQL, a known pair reserves delivery capacity before its authoritative
   MongoDB check; an invalid or canceled check releases any per-type reservation
   that has not already been replaced by refill. The shared send pays for the
   check. The webhook checks credentials before spending delivery capacity.
3. **Bounded fallback.** Unknown or unverifiable pairs use a separate budget of
   one MongoDB lookup per second, with burst and concurrent lookup limits of five.
   Only a successful lookup can spend delivery capacity. This lets a token
   created or rotated on another replica work before the directory refreshes.
   When the fallback budget is exhausted, GraphQL returns `deny by throttle`
   and the webhook returns 503: callers should retry. Under sustained guessing,
   first-use credentials may be delayed; known cached credentials retain their
   delivery capacity. Failed checks never create per-type delivery state.

The directory reads at most 10,000 oldest credentials and refreshes on misses at
most once per second, or immediately following a change in this process. Failed
reads retain known pairs and retry later. MongoDB remains authoritative for every
accepted credential. One read runs at a time, and at most 32 misses wait for it.
A miss past that limit does not wait: it answers from the copy as it stands, so
a pair the copy lacks takes the bounded fallback at once. A slow read therefore
holds at most 32 requests, however many arrive. Known pairs never wait. When
misses go past the limit, a WARN entry, written at most once a minute, reports
the limit and the running total.

Budgets are token buckets that refill from elapsed time when read; they run no
goroutine and need no shutdown. Every budget starts full, at its burst. Earlier
versions started one with a second's worth (`*_per_sec`) and refilled it to the
burst, so a start at the burst only matches what an idle type already had. A
per-type budget that has refilled fully
is dropped, which changes no decision because a missing budget is a full one, so
retained state is bounded by recent sends rather than by the number of alert
types. Validating the throttle settings at startup builds nothing. A GraphQL
delivery-budget refusal returns `deny by throttle`; the server logs which budget
refused at debug level and a throttle summary with counters at most once a minute.
Fallback saturation returns the same GraphQL error without that delivery-budget
summary.

Text success requires a provider acknowledgement whose text exactly matches the
sent message. Documents require a message ID and file ID; if file size is present
it must match. This confirms the provider's response contract, not a user's read
or an independently downloaded document. Missing, changed, oversized or malformed
responses produce an unconfirmed outcome without fallback or resend. Context
cancellation closes the actual request and prevents later recipients from starting.

API rejection, transport failure and partial fanout return visible GraphQL errors.
Errors contain safe counts/status/API code/retry-after metadata; they do not echo
bot-token URLs, raw API descriptions or application message bytes. Cancellation
and deadline causes remain discoverable through `errors.Is`. Rejected,
unconfirmed and unattempted recipients are counted separately from accepted ones.

There are **no automatic retries**, including for 429. A caller may explicitly
retry the full request, honoring a provider retry-after, but that can duplicate
recipients already accepted during a partial or ambiguous earlier attempt. Two
separate identical requests remain two events. No persisted idempotency/history
store is added. A successful SDK `Send` is still local queue admission; the SDK's
asynchronous sender does not create a recipient-delivery receipt.

## Cloud Monitoring webhook

`POST /webhook/gcp/alerts/cloud-monitoring` lets a Google Cloud Monitoring
notification channel reach an alert type directly, because Monitoring has no
Telegram channel and its webhook body cannot be shaped into the GraphQL mutation.
Inbound third-party webhooks live under `/webhook/<provider>/`.

The route is always mounted. When the Telegram service did not start, it answers
503, as the GraphQL mutation reports the service unavailable, so Monitoring sees a
retryable outage instead of a missing route. It follows the same internal prefix
and public-root fallback as the SSO routes:

| `settings.web` | Mounted paths |
| --- | --- |
| Neither prefix set (default `url_prefix` is `/mcp`) | `/mcp/webhook/gcp/alerts/cloud-monitoring` only |
| `url_prefix: /mcp`, `public_url_prefix: /` (the example and the deployed configuration) | `/mcp/webhook/gcp/alerts/cloud-monitoring` and `/webhook/gcp/alerts/cloud-monitoring` |
| Any other `public_url_prefix` | `<url_prefix>/webhook/gcp/alerts/cloud-monitoring` only |

Use whichever mounted path your ingress forwards to the gateway.

Use the `webhook_basicauth` channel type:

| Label | Value |
| --- | --- |
| `url` | `https://<gateway><mounted path>?env=<label>` |
| `username` | The alert type |
| `password` | The alert type's push token |

`env` is optional: 1–32 ASCII letters, digits, `_` or `-`. It labels each message and
falls back to the incident's `scoping_project_id`. `webhook_tokenauth` is not
supported, because it would put the push token in the URL. Refreshing an alert type's
token also requires updating the channel's password.

The receiver renders the incident as literal text and sends it through the same
subscriber path as `TelegramMonitorAlert`, so subscribers, the type header,
throttling and the acknowledgement rules above all apply unchanged:

```text
<alert_type> >>>>>>>>>>>>>>>>>>
[PROD] OPEN CRITICAL: <policy>
condition: <condition>
resource: <resource>
<summary>
started: <RFC 3339 UTC>
ended: <RFC 3339 UTC, closed incidents only>
<console URL>
```

Empty optional fields are omitted. The state word is derived, never guessed:

| Notification | State word |
| --- | --- |
| `state` is `open`, in any case | `OPEN` |
| `state` is `closed`, in any case | `RESOLVED` |
| `version` is `test` (the console's **Test Connection**, which says `OPEN`) | `TEST` |
| Any other or missing `state` | `UNKNOWN-STATE` |

`started_at` and `ended_at` accept `null`, `""`, `0` (all meaning absent) or
non-negative decimal Unix seconds, as a JSON number or string, up to
9999-12-31T23:59:59Z. A fraction is truncated. Any other spelling, such as an
exponent, sign, hex, `NaN` or a later year, is a schema violation and answers 400.

### Admission order

1. A request without credentials gets the 401 challenge Monitoring requires, with
   no lookup.
2. Both parts are normalized, then checked against the shared credential
   directory. Known pairs are checked against MongoDB. Unknown or unverifiable
   pairs use the separate bounded fallback described above; a valid remote
   rotation can therefore work immediately. An exhausted fallback answers 503
   so the sender retries. An invalid credential answers 401 after verification.
   A failed directory read retains known pairs, while a truncated read copies
   at most the oldest 10,000 alert types.
3. `env`, the body (1 MiB at most) and the JSON are checked, and only then are the
   shared and the alert type's send budgets spent, together. The request is
   already authenticated, so unlike `TelegramMonitorAlert` it reserves nothing
   before the MongoDB check, and neither budget is spent when either is empty.
   A throttled send answers 429 with
   `Retry-After: 1`, since every throttle bucket refills at least one send per
   second.
4. Delivery to all subscribers must finish within 20 seconds, well under
   Cloudflare's 125-second origin read timeout. Google publishes no webhook
   timeout. A send still running at the deadline is cancelled and the request
   answers 503.

Responses carry a status only:

| Status | Meaning | Sender should |
| --- | --- | --- |
| 204 | Every subscriber's send was acknowledged | Stop |
| 400 | Invalid `env`, unreadable body, invalid JSON, a timestamp outside the forms above, or no `incident` | Stop; a retry cannot help |
| 401 | Missing or refused credentials, with a `WWW-Authenticate` challenge | Send credentials; Monitoring needs this first response |
| 413 | Body over 1 MiB, refused rather than truncated | Stop |
| 429 | The alert type or total throttle denied the send; carries `Retry-After` | Retry later |
| 503 | Telegram service, authoritative storage or throttle unavailable; fallback lookup capacity exhausted; delivery deadline reached; or some subscribers not confirmed. Fallback exhaustion carries `Retry-After: 1`; provider retry information is forwarded when available | Retry; subscribers already accepted may see a duplicate |

Logs carry only the alert type, the derived state, and the incident ID when it
is 1–64 letters, digits, `.`, `_` or `-` (otherwise only its length). Policy,
condition, resource, summary, URL and timestamp values are never logged, and no
decode error quotes the body. Refused credentials are counted rather than
logged one by one: `refuse cloud monitoring credentials` is a WARN entry written
at most once a minute while credentials are refused, with `refused_total`
counting every refusal since startup. Alert on that message: while it repeats,
either a channel's password is wrong or someone is guessing. Guesses can consume
the separate bounded MongoDB lookup budget and delay credentials that are not
yet in the directory, including first-use or remotely rotated credentials.
Already cached pairs do not consume that fallback budget.

### Edge rate limit

A Cloudflare WAF rate-limiting rule for the path is recommended, with the
**Block** action, because Monitoring cannot solve a Managed Challenge. On
Business or Enterprise plans, count only `http.response.code eq 401`, so that
successful deliveries never count. On the Free plan the rule counts every
request per IP over 10 seconds. Either way, set the threshold well above
Monitoring's own bursts: one channel can receive up to 1,000 notifications at
once, and each may first draw a 401 challenge.

The rule only limits the request volume one address can send. It is not what
keeps channels delivering by itself: guessers below any threshold, or spread
over many addresses, can consume the bounded authoritative fallback budget.
That can delay credentials absent from the directory. The separate directory
refresh is normally limited to one read a second on misses, with an immediate
refresh after a local credential change; it is not the fallback lookup limit.
At most 32 requests wait for a refresh; the rest go straight to the fallback,
so a slow MongoDB read cannot accumulate waiting requests.

### Delivery guarantees

The durability is what Monitoring's webhook offers, and it is weaker than a Pub/Sub
subscription. Monitoring publishes no retry count or window for webhooks. Its only
statement found is a 2020 blog post saying it retries a few times and then drops the
notification. There is no queue here either, so an alert that arrives during a
gateway or storage outage, or that the send throttle defers past Monitoring's
retries, may never reach Telegram. A failed delivery shows up in Monitoring's
`notification_channel_events` log. Keep an independent channel, such as email, on
any policy whose alert must not be lost. If loss is unacceptable, use a Pub/Sub
channel and a worker that acknowledges only after delivery, rather than this route.
See [the source notes](../ref/cloud_monitoring_webhook_20261103.md).

To adopt it, run the old and new paths side by side before retiring one:

1. Release the gateway, then confirm through the production ingress that an
   unauthenticated `POST /webhook/gcp/alerts/cloud-monitoring` answers 401 with a
   `WWW-Authenticate: Basic` header.
2. Add a log alert on the refusal warning above, so a wrong channel password is
   noticed, and the recommended [edge rate limit](#edge-rate-limit).
3. Create the channel and click **Test Connection**. The subscribers should see a
   `TEST` message.
4. Add the channel to one policy next to the existing notification path, and compare
   one open and one resolved incident.
5. Add it to the remaining policies, then retire the old path.

## Qualification and release

Synthetic tests use only loopback HTTP, a synthetic bot token and synthetic IDs.
They cover full text/Unicode, 20 KiB and 1 MiB documents, byte/UTF-16 boundaries,
invalid inputs, token/throttle failures, partial fanout, duplicate rows, explicit
retry, ambiguous responses, real cancellation and safe errors. The opt-in
`GRAPHQL_TEST_ALERT_SDK_FIDELITY=1` campaign requires a temporary modfile selecting
the exact qualified SDK136 source; it tests the actual SDK hook through real
GraphQL and receiver HTTP. It does not silently upgrade the production module or
claim that the older pinned SDK preserves hook content. No real bot notification
is sent by these tests.

Keep the receiver fix Draft until its exact release is approved. Merging current
master automatically publishes the GraphQL image and deploys b1; a source-only
review is not deployment authorization. SDK136 merge approval, actual caller
configuration/adoption and production ingress/notification acceptance remain
separate gates for crawler92/75. Rollback pins the prior image through the normal
immutable procedure; no schema or persisted-data migration is required. Rolling
back reintroduces the former clipping defect, so receivers should be upgraded
before callers rely on complete large alerts. The schema and mutation arguments
are unchanged for old clients.
