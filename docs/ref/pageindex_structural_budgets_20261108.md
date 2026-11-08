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

Applied: `SpendGovernor` keeps a call bucket and a token bucket per tenant and one
global pair, refilled over `limits.window`. Calls reserve their estimated input
tokens before they are sent and settle to the provider-reported total afterwards
(debt is repaid by refill). A call that was never sent returns its operation budget
and its tokens but keeps its call spent, which cannot mint capacity.
