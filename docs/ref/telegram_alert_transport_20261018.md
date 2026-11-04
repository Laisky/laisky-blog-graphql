# Telegram alert transport references (2026-10-18)

Primary source: [Telegram Bot API](https://core.telegram.org/bots/api), checked
2026-10-18. The [sendMessage](https://core.telegram.org/bots/api#sendmessage)
contract limits text to 1–4096 characters after entity parsing. It does not define
that limit as a byte count or Go rune count. The receiver conservatively counts
UTF-16 units for text routing; supplementary characters consume two units.

[sendDocument](https://core.telegram.org/bots/api#senddocument) supports general
file uploads up to 50 MB. The documentation does not specify whether MB uses a
decimal or binary convention. Captions are optional and limited to 0–1024
characters after entity parsing; the alert transport sends no caption.
[Making requests](https://core.telegram.org/bots/api#making-requests) defines the
`ok`/`result` or error response. A successful API response acknowledges provider
acceptance, not that a recipient read the content. The transport does not use an
idempotency key and cannot promise exactly-once delivery after ambiguous failure.

Pinned local dependency `gopkg.in/telebot.v3 v3.3.8`, `api.go`, uses a background
context for ordinary requests and no request context for multipart sends.
`internal/web/telegram/service/service.go` previously exposed only a contextless
plaintext alert send. The new alert-only method uses `http.NewRequestWithContext`
with the same existing bot URL/token and retained HTTP client. Other bot sends
retain their existing behavior. There is no shared-client mutation or background
upload goroutine. Multipart headers/footer are small in-memory buffers, and the
complete message is read into the HTTP stream without an application file.

The local 2 MiB complete-message budget is an explicit application admission
limit below the documented document ceiling. It is not an HTTP body cap: gqlgen
currently decodes the GraphQL POST before resolver validation. That existing
preparse allocation/error-presentation behavior and deployed proxy limits are
not changed or qualified by this transport repair. Unrelated GraphQL operations
therefore retain their ingress behavior. No archive, Arweave upload or custom
storage API is involved.
