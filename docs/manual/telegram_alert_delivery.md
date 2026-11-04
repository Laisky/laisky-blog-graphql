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
There is no application file, persistent archive, new endpoint or new recipient.
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
