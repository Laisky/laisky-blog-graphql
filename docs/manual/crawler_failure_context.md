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

## Recoverable private failure logs

Authenticated failed web_fetch operations synchronously save an application-native
JSON failure-log record. The original declared URL is retained before trimming;
the submitted URL is separate. Only the declared output_markdown value and its
presence are selected from arguments, with the effective boolean recorded
separately. Missing, explicit null, false and true remain distinguishable.
HTTP request headers, authorization context, provider API keys and unknown tool
arguments are not serialized. Calls without a nonempty declared URL, authorization, or a valid output option,
and successful operations, do not create caller failure records.

Each record identifies its emitting service, hostname and process run, request ID
and the task ID when one was assigned. failure.stage means the phase observed by
that emitter, not an assertion about an upstream root cause. A caller receiving
crawler_failed records worker_result, while the public legacy stage remains
render. Optional worker_stage/worker_code preserve the bound validated worker
report; a worker records its actual phase/code without duplicating these fields. A caller failure before queue submission
has no invented task ID. The paired worker failure log contains the exact byte
sequence passed to the converter, rather than a later DOM reconstruction or only
an HTML preview. Records join on the existing task identity; logging does not
alter task/policy/output binding, public error codes or successful body release.

The journal stores byte payloads reversibly as JSON base64, with explicit states,
lengths and SHA256 checks. Atomic private-file publication and filesystem sync
precede a complete reference. Partial, corrupt or failed writes are not reported complete; a final file left
after an unsuccessful directory sync is reported durability_uncertain. A complete
JSON record alone does not establish storage persistence. Journal code does not redact, truncate
or delete payloads. Existing response budgets still apply to crawl behavior; they
do not authorize silently clipping diagnostic bytes.

CRAWLER_FAILURE_LOG_DIR selects an explicit private journal directory. Without
it, the caller uses the process temporary directory's
laisky-blog-graphql-crawler-failure-logs subdirectory (normally
/tmp/laisky-blog-graphql-crawler-failure-logs), with private directory/file
permissions. This default is container-local and can disappear
on container replacement; it is not durable external storage or an indefinite
retention guarantee. A persistent mount, operational retention/capacity policy
and complete capture/retrieval acceptance require separate rollout qualification.
No deployment configuration is changed by this source patch.

Alerts put emitter identity, capture status/ID, digest and size first; the full
path follows operation details. A long path therefore cannot push the bounded
recovery identity out of a notification preview. The journal and complete path
are not truncated.
The current SDK diagnostic filter and Telegram length/queue/throttle behavior
remain unsuitable as a full-payload archive. Recovery uses the private failure
logs themselves, without re-fetching the target, reading provider configuration
or making paid calls. Synthetic tests establish local capture and reference
delivery; they do not establish production retention or recover already expired
historical records. Missing old inputs are an instrumentation/retention gap.

## Required deployment gate before merging this change

The default branch normally publishes and deploys after merge. This source-only
change must therefore stay Draft until an explicit approved persistent private
journal configuration is prepared and qualified for both services. The default
temporary directory is not production durability acceptance.

The future deployment must provide a persistent private bind/directory through
CRAWLER_FAILURE_LOG_DIR, owned by the actual service identity. The caller runs
as UID 1000 and needs its own 0700 directory and 0600 journal files; the worker's
separate identity/directory must be qualified independently. The filesystem must
support non-overwriting hardlink publication, file sync and directory sync.
These requirements do not authorize changing a shared directory's ownership or
permissions.

Capacity budget, monitoring, capture-write failure handling and the owner-managed
retention policy must be explicit. The application performs no automatic deletion.
Container recreation and application/image rollback must preserve existing
archives. Qualification must prove a saved paired capture remains readable after
the approved lifecycle transition, without fetching the failed URL or consulting
provider configuration. No VPS/Compose/mount change or production persistence
qualification was performed as part of this source implementation.
