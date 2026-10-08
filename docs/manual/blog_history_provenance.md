# Public article history provenance

`BlogPostHistory` accepts a syntactically valid Arweave ID, then authorizes it from current trusted Mongo publication metadata **before** any request to the fixed archive gateway. An arbitrary gateway JSON object does not establish publication provenance. Only IDs recorded in a post's `arweave_id.id` array by the normal archive publication path are eligible.

Current `hidden: true` and nonempty `post_password` deny public history, consistent with the current public reader's body protection. An explicit `post_status` other than `publish` also denies history. Registered legacy records with a missing or empty status remain eligible: the existing public reader's `makeQuery` has no status predicate, and legacy Python archive support predates the current Go publisher, which explicitly writes `publish`. This exception is limited to a successful trusted membership lookup; it does not trust status in downloaded archive JSON.

A missing record, unavailable database, or malformed metadata fails closed. Removing a post record also removes public history authorization, even if an immutable archive remains at the gateway. Each history view now needs current publication metadata and a gateway fetch; a browser's old `postHistory` cache entry is not sufficient authorization. The companion frontend change always asks the server and ignores legacy cached historical bodies. Current article caching remains unchanged.

Authorized archive contents are returned unchanged. This preserves source-authored iframes, Slide HTML, styles, media, SVG/MathML, current and legacy JSON, `gz::` compressed bodies, and English translations. This change does not introduce a new HTML policy or claim that intentionally authored active HTML is forbidden.

## Local regression evidence

The first retained regression returned synthetic iframe-canary JSON through a mocked HTTP transport while Mongo returned no registered archive. The original loader returned the foreign body successfully; the repaired loader rejects it without invoking the transport. No live gateway, account, email, or blockchain transaction is involved.

`post_history_security_test.go` exercises unknown, unrelated, never-published, database-error, hidden, password-protected, draft, future, and malformed IDs. It checks the actual Mongo wire filter and projection before the local gateway transport, and confirms exact authorized HTML/Slide preservation for current JSON, legacy JSON, gzip and translations, including registered legacy metadata without status.

Run the focused regressions with:

```sh
GOMAXPROCS=2 go test -mod=readonly -p 1 -count=1 -timeout45s ./internal/web/blog/service -run '^TestPostHistory' -v
```
