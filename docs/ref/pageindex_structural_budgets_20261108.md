# PageIndex structural and spending budgets: reference notes

Checked: 2026-11-08, for issues #85 and #91. Measurements were taken locally on
go1.27.3/linux-amd64 with the module versions pinned in `go.mod`
(`github.com/yuin/goldmark v1.8.6`, `github.com/tiktoken-go/tokenizer v0.8.1`).
Repository decisions are in
[the operator manual §6.6](../manual/mcp_memory_plugins.md#66-structural-and-spending-limits).

## Markdown parsing (goldmark)

| Ref | Source | Fact and application |
| --- | --- | --- |
| M1 | Local measurement, `goldmark.New().Parser().Parse` v1.8.6 | Super-linear on untrusted input: `"*a_ "` repeated takes 1.5 s at 80 KB and 82 s at 320 KB; unclosed links `"[a](<b"` 0.7 s at 120 KB and 11 s at 480 KB; nested brackets 0.2 s at 40 KB and 1.7 s at 160 KB; consecutive link-reference definitions 0.26 s at 220 KB and 11.5 s at 880 KB; one line of `>` 0.18 s at 20 KB and 2.7 s at 80 KB; one line of `"- "` 1.7 s at 40 KB and 21 s at 160 KB. The 2 MB `file_write` payload limit does not bound this work. |
| M2 | [goldmark releases](https://github.com/yuin/goldmark/releases), [issues](https://github.com/yuin/goldmark/issues?q=quadratic) (#555, #596, #608, #611, #629, #631, #635) | v1.8.7 (2026-10-30) does not change these paths. v2.1.15 (2026-11-02, new module path `github.com/yuin/goldmark/v2`, new API) makes the emphasis, unclosed-link and bracket cases linear, but `linkReferenceParagraphTransformer.Transform` still copies the remaining paragraph lines once per removed definition (quadratic). No goldmark denial-of-service advisory was found in the [Go vulnerability database](https://pkg.go.dev/vuln/). An upgrade alone is therefore not sufficient. |
| M3 | goldmark v1.8.6 source: `parser/parser.go` `parseBlock`, `parser.InlineParser` | Inline parsers receive the block being parsed as `parent`; returning `nil` leaves the trigger byte as literal text. Block structure, setext headings and link-reference extraction are decided in the block phase, before inline parsing, and every block opened by the parser records `Pos()` (including empty ATX headings, which have no content segment). |
| M4 | [cmark](https://github.com/commonmark/cmark) `src/inlines.c`, [pathological tests](https://github.com/commonmark/cmark/blob/master/test/pathological_tests.py) | The reference implementation keeps parsing linear with an `openers_bottom` table per delimiter kind, a 999-character link-label cap, and a cap on link-reference expansion; its pathological-input suite runs in CI. |
| M5 | [league/commonmark security](https://commonmark.thephpleague.com/2.2/security), [pulldown-cmark performance](https://pulldown-cmark.github.io/pulldown-cmark/dev/performance.html) | Untrusted Markdown needs an input-size cap, a nesting cap (10-50 levels is the documented range), bounded concurrency, and doubling-size regression benchmarks; a timeout alone cannot stop an uncancellable parse. |

Applied: PageIndex parses with goldmark's default block parsers and paragraph
transformers (identical block structure and headings) but runs the inline parsers
only inside headings whose source is at most 1 KiB (M3), takes line numbers from
one newline index, and sends documents whose container nesting (more than 32
markers on a line, or a list item prefix over 256 bytes) or link-reference layout
(more than 2^24 estimated line copies) would still drive the block parser
super-linear to a linear ATX scanner (M1, M2, M5). The previous extractor is kept
as a test oracle over the repository's own documents.

### Block-level work (review of #96, 2026-11-10)

| Ref | Source | Fact and application |
| --- | --- | --- |
| M6 | Local measurement, heading-only goldmark parse, 2 MB inputs | Block structure alone still costs seconds and hundreds of MB: a tab-indented list 254 deep 3.7-5.5 s and 801 MB; `"- a\n"` 0.8-1.8 s and 543 MB; `"*\n"` 0.6-1.0 s and 441 MB; `"#\n"` 0.46 s and 315 MB; items at depth 4 0.5-0.8 s and 370 MB; one list item followed by blank lines 1.4-1.7 s and 520 MB; `"a\n\n"` 0.66-0.9 s and 330 MB. Plain prose of the same size: 33-50 ms and 11 MB. Per unit: about 1 KB of AST per list item, 470 B per paragraph, 300 B per heading, 320-750 B per content line (fenced, quoted, list continuation), 250 B per blank line inside an open list; blank lines at top level are free. |
| M7 | goldmark v1.8.6 `parser.Parse` | `Parse` takes no context, so neither cancellation nor a deadline can stop a parse in progress; bounding its input before the call is the only control. |

Applied: one linear pre-scan classifies lines (fenced and indented code, HTML blocks
of the seven CommonMark types, thematic breaks, ATX and setext headings, list items,
block quotes, paragraphs), measures indentation in columns with tab stops of 4, and
sends a document to the linear scanner when a container line nests deeper than 32,
the summed nesting exceeds 400,000, or it has more than 40,000 blocks or 80,000
content lines. Under those bounds a 2 MB document costs at most about 60 ms and 40 MB.
The linear scanner renders titles with goldmark, one heading at a time and with the
document's link-reference definitions, and matches goldmark on all 108 documents of
the repository corpus. Inline parsing is limited to 256 KiB of heading source and
32 Ki markup characters per document, because 2,000 headings of 1 KiB of `[` cost
1 s and 114 MB otherwise; with the limits, at most about 160 ms.

## Token counting (tiktoken-go)

| Ref | Source | Fact and application |
| --- | --- | --- |
| T1 | Local measurement, `tokenizer.Get(o200k_base).Count` v0.8.1 | Quadratic in the length of one pre-tokenized piece: 16 K `"ab"` pairs take 1.4 s, 16 K CJK runes 1.5 s, 16 K spaces 0.23 s; 1 MB of one letter did not finish in two minutes. Ordinary prose costs about 4 MB/s. |
| T2 | tiktoken-go v0.8.1 `codec/*.go` | Pre-tokenization uses `github.com/dlclark/regexp2` (backtracking, with lookahead) and each piece is merged by byte-pair encoding, so cost grows with the longest run without a whitespace boundary. |

Applied: `Count` passes text through unchanged unless a run of whitespace or
non-whitespace bytes exceeds 256 bytes; such runs are counted in 256-byte segments
on rune boundaries. Counts of ordinary text are unchanged; a cut can add at most one
token, which keeps estimates conservative.

## Spending budgets

| Ref | Source | Fact and application |
| --- | --- | --- |
| S1 | [`library/throttle`](../../library/throttle/throttle.go) (Telegram alert admission) | The repository's established admission pattern: lazily refilled token buckets, one shared and one per key, admitted in one step; a refusal spends nothing; a bucket that has refilled completely is dropped because dropping it changes no decision. |
| S2 | [golang.org/x/time/rate](https://pkg.go.dev/golang.org/x/time/rate#Reservation.CancelAt) | Reservation and cancellation semantics for token buckets; a refund must not mint capacity that later reservations already consumed. |

| S3 | [Linux ext4 reserved blocks](https://www.kernel.org/doc/html/latest/admin-guide/ext4.html) (`resuid`/`resgid`, `reserved` space) | A standard way to keep a shared pool usable for a protected class: the last share of capacity is only for that class, while the total stays fixed. |
| S4 | [OpenAI API error codes](https://developers.openai.com/api/docs/guides/error-codes) (checked 2026-11-10) | Rate-limit 429s and 500/503 are retried after `Retry-After` or with exponential backoff; 400 and 401 are request or credential problems and 403 an unsupported region, which fail again unchanged. Quota or billing 429s are not restored by retrying, so the small default attempt count bounds them too. |

Applied: `SpendGovernor` keeps a call bucket and a token bucket per tenant and one
global pair, refilled over `limits.window`. Every upstream request, retries included,
reserves its estimated input tokens before it is sent and settles to the
provider-reported total afterwards (debt is repaid by refill). A call that was never
sent returns its operation budget and its tokens but keeps its call spent, which
cannot mint capacity. A bucket with a request in flight is never swept, so a late
settle charges the bucket that admitted it. The last `global_reserve_percent` of the
global budget is reserved for tenants under one document's allowance in the window
(S3). Only timeouts, 408, 409, 429 and 5xx are retried (S4). Tests inject the
governor's clock, so no budget decision depends on how long a test runs.
