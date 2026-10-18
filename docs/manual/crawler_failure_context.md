# Crawler failure context

The public MCP result remains fetch failed: crawler_failed, and the internal
FetchFailureDiagnostic.Stage remains render for existing consumers. That
legacy stage identifies the caller's crawler boundary; it does not prove that
browser rendering failed. Production diagnostics now separately author
caller_phase=worker_result and caller_stage=render, and use the validated
worker stage as the alert-facing stage and effective_worker_stage when known.
Legacy failures without worker stage remain unspecified at the worker layer.

The optional Redis failure_context object carries observed scalar metadata.
Counts are nullable int64 values: absent means unobserved, while zero is an actual
observation. Rendered HTML, conversion input, conversion output and trimmed output
are different byte counts. body_text_runes and body_text_nodes describe parsed
non-whitespace body text outside script/style elements, not CSS-visible text.
The converter identifier is html-to-markdown/v2. Observed overrun counts remain
available even when the existing response budget rejects the body.

Primary-document HTTP status, base MIME type, redirects and final scheme/hostname
are optional and must come from an actual worker observation. They are never
inferred from empty_markdown, a duration, a missing receipt or HTTP metadata for
a subresource. The caller separately selects only target scheme/hostname from its
admitted URL. No full URL, path, query, userinfo, cookies, headers or page body is
added to diagnostic fields. These are application-selected metadata components;
there is no shared SDK policy change or filter evasion.

Context is attributed only after the existing exact task ID, target URL, output
mode and policy binding. Invalid optional envelopes are not attributed. Successful
body release still requires the existing egress receipt, body budgets and nonempty
result checks. Diagnostics are not connection proof and cannot change a failed
result into success. Older workers and decoders remain compatible.

Each hostname is bounded at 253 bytes, base MIME at 127 bytes, status at 100..599
and redirects by the existing maximum of 20. Counts accept all nonnegative int64
observations without clamping. The complete fixed-field envelope fits a 2048-byte
wire bound, including valid maximal fields. Metadata copies do not alias worker
DTO count pointers. No response, security, billing or retention limit is changed.

Tests exercise the actual production WebDiagnosticAlertHook and default SDK
NewAlert through local HTTP, including zero/absent counts, maximal components,
unknown application fields and legacy public errors. These synthetic tests do not
identify the historical target's upstream cause, prove real Telegram delivery or
constitute production acceptance. No failed target is retried by this change.
