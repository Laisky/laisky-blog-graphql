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

Applied: `SpendGovernor` keeps one call and token budget per tenant and one global
pair over `limits.window`. They were token buckets (S1) until the third review of
#96, and are now strict sliding windows (see "Rolling windows" below). Every upstream request, retries included,
reserves its estimated input tokens plus an output allowance before it is sent and
settles at completion (see "Output ceilings and settlement" below). A bucket with a
request in flight is never swept, so a late settle charges the bucket that admitted it. The last `global_reserve_percent` of the
global budget is reserved for tenants under one document's allowance in the window
(S3). Only timeouts, 408, 409, 429 and 5xx are retried (S4). Tests inject the
governor's clock, so no budget decision depends on how long a test runs.

### Output ceilings and settlement (second review of #96, 2026-11-15)

Sources were checked on 2026-11-15.

| # | Source | Finding |
| --- | --- | --- |
| S5 | [OpenAI reasoning guide](https://developers.openai.com/api/docs/guides/reasoning); `max_output_tokens` docstring in [openai-python `response_create_params.py`](https://raw.githubusercontent.com/openai/openai-python/main/src/openai/types/responses/response_create_params.py) | `max_output_tokens` bounds visible output and reasoning tokens together. At the bound the response is `status: "incomplete"` with `incomplete_details.reason: "max_output_tokens"`, and input and reasoning are billed even when no visible text was produced. The guide suggests reserving about 25,000 tokens for reasoning and output when starting out. The API rejects values below 16. |
| S6 | [gpt-5.4-mini model page](https://developers.openai.com/api/docs/models/gpt-5.4-mini) | gpt-5.4-mini supports reasoning efforts none (the default), low, medium, high and xhigh. Some other GPT-5 models default to medium, so the reasoning share of the output ceiling depends on the configured model. |
| S7 | [OpenAI rate limits guide](https://developers.openai.com/api/docs/guides/rate-limits); [OpenAI background mode](https://developers.openai.com/api/docs/guides/background) | OpenAI's own rate limiter counts a request as the larger of `max_tokens` and an estimate from its character count, at admission. Unsuccessful requests count toward rate limits. A synchronous request is canceled only by closing the connection, and OpenAI publishes no policy on billing work already done for timed-out or canceled requests. |
| S8 | [LiteLLM input/output token rate limits](https://docs.litellm.ai/docs/proxy/io_token_rate_limits) | Gateway practice of reserving and then reconciling. LiteLLM reserves the estimated input plus an output cap: the request's own cap, else the deployment's, else a fraction of the limit. It reconciles to reported usage, and a response that reports no usage keeps its reservation. Envoy AI Gateway and Kong charge only after the response, so admitted requests can overshoot. |
| S9 | [golang.org/x/time/rate `CancelAt`](https://raw.githubusercontent.com/golang/time/master/rate/rate.go) (v0.16.0); GCRA in [throttled](https://raw.githubusercontent.com/throttled/throttled/master/rate.go) | A late correction first advances the bucket to the settlement time, capping the refill at its capacity, and only then applies the restore or debit. GCRA's `max(tat, now)` is the same rule. |

Applied:

- The gate grants each request an output allowance, sent as `max_output_tokens`, and
  reserves it with the input estimate in the operation, tenant and global budgets
  (S7, S8). The estimate includes the message framing and the output schema.
- There are two ceilings, because ceilings that are too large make concurrent calls
  contend for small per-document budgets:
  - `max_output_tokens_per_call` (4096) is for short answers.
  - `max_toc_output_tokens_per_call` (32768) is for answers that re-emit a table of
    contents. It covers about a thousand entries and S5's 25,000-token suggestion.
- An allowance is lowered to what the operation has left after its input. A request
  that cannot be granted 256 tokens is refused. Calls take their concurrency slot
  before they reserve.
- Each request is charged at completion:
  - the reported usage, when there is one;
  - for an answer without usage, the input estimate plus the answer's estimated tokens;
  - for an error response, the input estimate;
  - nothing for a request that never left the process;
  - for a request the provider never answered, its whole reservation, since S7 makes
    billing of timed-out work an open question.
- A failed request is settled before its retry is reserved, so its unused allowance
  is available to the retry.
- An answer cut at the ceiling is never used or cached (S5). It is detected by the
  incomplete status, or by an answer that used the whole ceiling.
- `spendTicket.settle` refilled both buckets to the settlement time before it
  applied the correction (S9); the sliding windows below keep the property by
  charging debt at settlement time. Before this fix, a call admitted at 1 token of a 100-token
  bucket and charged 100 tokens a full window later left the bucket full instead of
  holding 1 token.

Measured on 2026-11-15:

- One document with a 200,000-token ceiling, against a provider that answers with
  150,000 output tokens, was billed 300,156 tokens in 2 calls. With output reserved
  it was billed 199,968 tokens in 48 calls, and two documents of one tenant stayed
  within its 300,000-token window.
- In the fallback scanner, rendering 2000 reference-link titles with 512 retained
  definitions took 1.39 s, against 9.7 ms with one definition. Each title re-parsed
  every definition. The definitions are now parsed once into a goldmark context that
  every title's context delegates to (goldmark keeps the first definition of a
  label, as before), and the render takes 5.7 ms.

## Third review of #96 (2026-11-15)

Sources were checked on 2026-11-15.

### Rolling windows

| # | Source | Finding |
| --- | --- | --- |
| W1 | [Anthropic rate limits](https://platform.claude.com/docs/en/api/rate-limits); [golang.org/x/time/rate](https://pkg.go.dev/golang.org/x/time/rate); [Stripe rate limiters](https://stripe.com/blog/rate-limiters) | Rate limits are token buckets, which bound the long-run rate and the burst, not the total in a window: a full bucket of capacity C spent at once refills while spent, so about 2C can be admitted within one window. |
| W2 | [Cloudflare sliding window counter](https://blog.cloudflare.com/counting-things-a-lot-of-different-things/); [Upstash algorithms](https://upstash.com/docs/redis/sdks/ratelimit-ts/algorithms); [limits strategies](https://limits.readthedocs.io/en/latest/strategies.html) | The weighted two-window counter is documented as an approximation; only a sliding log ("moving window") is exact. |
| W3 | [Figma, an alternative approach to rate limiting](https://www.figma.com/blog/an-alternative-approach-to-rate-limiting) | A ring of 60 one-minute counters for an hourly limit is lenient when it sums only the last 60; Figma sums "the last hour and one minute", preferring to be slightly harsher. |
| W4 | [LiteLLM budgets](https://docs.litellm.ai/docs/proxy/users) | LLM gateways reserve the estimated maximum cost before the call, reject a reservation that does not fit, and replace it with the actual cost afterwards; budgets themselves use fixed windows. |

Applied:

- Each rolling budget is a ledger of what it admitted per slot of `window/60`, and a
  request is admitted only when the last 61 slots plus its reservation fit. Every
  interval of the window's length lies within 61 consecutive slots, so no such
  interval admits more than the ceiling (W3; the proof: the check at the last
  admission of an interval counted every earlier admission of that interval). A
  reservation counts for between one window and one window plus one slot.
- A refund is returned to the admission slot and dropped once that slot has left the
  window; a refund booked later would credit windows that never held the charge.
  Debt is recorded in the settlement slot, so it counts for a whole window from when
  it is known (this keeps finding A of the second review).
- Ledgers hold only non-empty slots, so their size is bounded by the calls the global
  budget admits per window; an empty ledger with no call in flight is dropped.
- Measured with `TestRollingBudgetIsAStrictCeilingOverEveryWindow` (random
  admissions, refunds and delayed settlements over eight windows): the token buckets
  admitted 120 calls within one 60-minute interval against a 90-call ceiling; the
  ledgers never exceed a ceiling.

### Provider contract

| # | Source | Finding |
| --- | --- | --- |
| P1 | [OpenAI Responses create reference](https://developers.openai.com/api/reference/resources/responses/methods/create/index.md) | `max_output_tokens` bounds visible and reasoning tokens, minimum 16. `status` is one of completed, failed, in_progress, cancelled, queued, incomplete; `incomplete_details.reason` is max_output_tokens, max_messages, content_filter or steered. Usage has input, output and total tokens. An unsupported parameter is a 400 `invalid_request_error` naming the `param`. |
| P2 | Laisky/one-api source at `b10e089d` (2026-11-14): `relay/controller/response_utils.go`, `response_billing_policy.go`, `response_io.go`, `relay/adaptor/openai/responseapi_convert_request.go`, `relay/controller/response_convert.go` | OpenAI channels on api.openai.com pass `max_output_tokens` through (allowlisted; unknown top-level fields are deleted, not rejected). Other channels are bridged to Chat Completions: the ceiling becomes `max_completion_tokens` (Claude `max_tokens`, Gemini `maxOutputTokens`), and a `length` finish becomes `incomplete` with reason `max_output_tokens`. Usage is carried over; some adaptors recompute the total. |
| P3 | [QuantumNous/new-api](https://github.com/QuantumNous/new-api) `relaykit/relayconvert`; LiteLLM `litellm/responses/litellm_completion_transformation/transformation.py` | Both map the ceiling and map `length` back to `incomplete`/`max_output_tokens`. LiteLLM reports `incomplete` with `incomplete_details: null` when the finish reason is missing. |
| P4 | vLLM `vllm/entrypoints/openai/responses/protocol.py` | The ceiling is clamped to the context left and the clamped value is echoed in the response's `max_output_tokens`; every incomplete answer reports `max_output_tokens`. |
| P5 | songquanpeng/one-api `router/relay.go` | The upstream one-api has no `/v1/responses` route, so it cannot serve PageIndex at all. |

Applied:

- `TestResponsesProviderContract` pins the request (model, input,
  `max_output_tokens` equal to the reserved allowance) and the classification and
  charge of every answer shape above, through the gate, on a loopback server.
- An answer is truncated when it is marked incomplete for `max_output_tokens` or used
  the whole ceiling, compared with the lower of the requested and the echoed ceiling
  (P4). It is incomplete for any other status than `completed` (P1, P3), such as a
  content filter or a missing reason; an answer without a status is complete.
  Neither is used or cached; both degrade their work instead of failing the run.
  Before, a `content_filter` or reason-less incomplete answer was used and cached.
- A missing `total_tokens` is the sum of input and output (P2). An output above the
  ceiling (an upstream that ignored it) is charged as debt and logged at most once a
  minute as `pageindex.llm.output_ceiling_ignored`.
- `llm.base_url` names a gateway by its origin in the deployed settings, as
  `settings.openai.base_url` and the RAG embedder do (the embedder strips and
  re-appends `/v1`). The SDK appended `responses` to it as given, so every request
  reached the one-api web UI and failed with an HTML answer; the live contract test
  found this. An origin now selects its `/v1` root, and a base URL with a path of its
  own is kept.
- `TestLiveResponsesProviderContract` checks the same contract against a real
  endpoint when `PAGEINDEX_CONTRACT_BASE_URL` and `PAGEINDEX_CONTRACT_API_KEY` are
  set. On 2026-11-15 the deployed gateway answered `/v1/responses` with JSON; the
  test key had no quota (`insufficient_token_quota`), so the answer-side assertions
  remain to be run with a funded key.

### Search fallback

- When the planner cannot run, search matches the query's terms against section
  titles and summaries (no LLM call, at most 32 terms and 4 KiB per field) and
  returns at most three non-overlapping matching sections, ranked below planned
  results, instead of always the first section. Markdown sections are keyed by
  heading line, which the old first-section range (page 1) missed whenever a
  document did not start with a heading.
- Planner ranges were resolved by iterating every page number in them, so a range
  ending near `math.MaxInt`, which the document's content can steer the planner to
  return, never finished; ranges are now resolved against the tree's sorted pages.

### Per-run usage (P3)

- `Stats.LLMCalls` counted only calls that returned a response, so a retried call or
  a request that got no answer spent budget its run did not show. The gate's single
  settlement point now records, through the operation budget, every request sent
  (`UpstreamRequests`), the retries among them, and the tokens charged for each,
  split into provider-reported and estimated charges; their sum equals what the
  rolling budgets were charged. `Index` returns these statistics with a run error.
