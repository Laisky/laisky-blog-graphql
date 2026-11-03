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
2. The credential is checked once. A check that does not authenticate is charged
   to a refusal budget shared by all callers: a burst of 20, refilled at 5 per
   second. While it is spent, credential checks wait with 429 and `Retry-After`
   instead of reaching MongoDB. Accepted credentials are never charged. Google
   publishes no sender addresses, and the gateway trusts `X-Forwarded-For` from
   any peer, so the budget cannot be per caller.

   So that guessing cannot lock a channel out, the receiver remembers each alert
   type's credential once it has authenticated, as a keyed digest in memory only,
   following OWASP's device-cookie defense against lockout abuse. A remembered
   credential is not held back by a spent budget, but it is still checked against
   MongoDB on every request, so a rotated or revoked token is refused at once and
   loses its exemption at that first refusal. Wrong guesses never match, so they
   cannot evict it, and a storage outage does not count as a refusal. The memory
   is per process: after a restart, a channel waits like any caller until its
   first successful delivery. Per-address limits belong at the edge; see below.
3. `env`, the body (1 MiB at most) and the JSON are checked, and only then is the
   alert type's send budget spent. A throttled send answers 429 with
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
| 429 | Refusal budget spent, or the alert type or total throttle denied the send; carries `Retry-After` | Retry later |
| 503 | Telegram service, storage or throttle unavailable, delivery deadline reached, or some subscribers not confirmed; carries `Retry-After` when Telegram gave one | Retry; subscribers already accepted may see a duplicate |

Logs carry only the alert type, the derived state, and the incident ID when it
is 1–64 letters, digits, `.`, `_` or `-` (otherwise only its length). Policy,
condition, resource, summary, URL and timestamp values are never logged, and no
decode error quotes the body. Deferred credential checks are counted rather than
logged one by one: `defer cloud monitoring credential checks after repeated
refusals` is a WARN entry written at most once a minute while checks are being
deferred, with `deferred_total` counting every deferral since startup. Alert on
that message: while it repeats, someone is guessing and any channel that has not
delivered since the last restart is waiting.

### Edge rate limit

Add this rule before you create the channel. Add a Cloudflare WAF rate-limiting
rule for the path, with the **Block** action:
Monitoring cannot solve a Managed Challenge. On Business or Enterprise plans,
count only `http.response.code in {401 429}`, so that successful deliveries never
count. On the Free plan the rule counts every request per IP over 10 seconds.
Either way, set the threshold well above Monitoring's own bursts: one channel can
receive up to 1,000 notifications at once, and each may first draw a 401
challenge. The rule is what keeps one guessing address from spending the refusal
budget. The in-process exemption covers only channels that have already
delivered.

### Delivery guarantees

The durability is what Monitoring's webhook offers, and it is weaker than a Pub/Sub
subscription. Monitoring publishes no retry count or window for webhooks. Its only
statement found is a 2020 blog post saying it retries a few times and then drops the
notification. There is no queue here either, so an alert that arrives during a
gateway outage, or that the refusal budget or throttle defers past Monitoring's
retries, may never reach Telegram. A failed delivery shows up in Monitoring's
`notification_channel_events` log. Keep an independent channel, such as email, on
any policy whose alert must not be lost. If loss is unacceptable, use a Pub/Sub
channel and a worker that acknowledges only after delivery, rather than this route.
See [the source notes](../ref/cloud_monitoring_webhook_20261103.md).

To adopt it, run the old and new paths side by side before retiring one:

1. Release the gateway, then confirm through the production ingress that an
   unauthenticated `POST /webhook/gcp/alerts/cloud-monitoring` answers 401 with a
   `WWW-Authenticate: Basic` header.
2. Add the [edge rate limit](#edge-rate-limit) and a log alert on the deferral
   warning above. Both are required before any policy depends on the channel.
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
