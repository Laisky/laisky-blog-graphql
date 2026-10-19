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
admitted URL. No full URL or page body is added to this optional Redis object. Original
selected input is separately emitted in ordinary failure logs and alerts.
Arbitrary cookies, headers and API keys are not selected; there is no shared SDK
policy change or filter evasion.

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

## Original input in ordinary logs and alerts

Authenticated failed web_fetch operations emit the original decoded URL before
trimming, submitted URL, declared output_markdown presence/value and effective
boolean through the existing structured logger and alert hook. Missing,
explicit null, false and true remain distinguishable. Successful operations
retain their existing behavior. No task, HTTP request envelope, authorization
context, provider API key or unknown argument is serialized.

Every failure entry identifies service, hostname, process run, public build
provenance, request ID and task ID when assigned. The emitter's stage does not
assert an upstream root cause. A caller observing crawler_failed records the
worker-result phase while the public legacy stage remains render; the validated
worker stage/code stay separate. A pre-submission failure invents no task ID.
Worker logs provide the last successfully observed browser DOM snapshot, the
single argument passed
to conversion and available partial renderer response observations. An observed
DOM snapshot can precede a later renderer failure and is not necessarily
returned, final or stable. Correlation does not change task/policy/output binding, public errors or successful body
release.

Selected original values are not application previews and are not silently
filtered, replaced or truncated. There is no application-managed journal,
capture-directory environment setting, payload archive API, replay CLI or
persistent mount requirement. Normal logging representation and delivery remain
the responsibility of the existing logger/SDK/receiver/destination.

Complete delivery must be qualified through that actual path, including large,
Unicode, empty/unobserved and binary inputs. A local HTTP test does not prove
real Telegram delivery or backend retention. Existing transport constraints must
remain observable; no application storage fallback circumvents them. Historical
missing input is an instrumentation/retention gap, not evidence of a particular
website cause and not information the user should have to reconstruct.

## Release build identity

ProcessSource selects Go's public build metadata: revision/modified status when
available, module/Go versions, service, hostname and a once-per-process run ID.
The release's local Docker context retains only the Git HEAD/index, refs,
packed refs, shallow boundary and local objects required by standard VCS stamping.
Tracked .gitignore is retained. Checkout configuration/auth, hooks, logs, linked
worktree/module metadata and external object-store hints are excluded. Git remains
in the build stage; the runtime stage copies the binary, not the Git directory.

The backend builds the module package with `go build ... .`. A named `main.go`
build does not supply this module's automatic VCS identity. Both select the same
sole root production Go source. A complete release checkout must produce its exact
revision and truthful modified bit. A manual linked-worktree pointer whose Git
metadata is outside the Docker context remains buildable with unavailable
revision; it does not qualify as a revision-identified release. No revision is
invented from environment configuration or a mutable image tag.

## Required delivery qualification

The default branch normally publishes and deploys after merge. Keep the Draft
until exact-source tests and production-path logging/alert fidelity are
qualified. This application change adds no storage/configuration mounts or
new listener. Independent logging changes remain owned and reviewed separately;
do not describe successful source fixtures as complete production acceptance.


## Existing HTTP alert delivery

Startup attaches the shared SDK's faithful alert core before application loggers
bind context. Reviewed crawler/operation scopes retain their chosen bound and
call-site fields, including namespaces, binary values and original URLs. The
application explicitly selects a 1 MiB complete-message budget instead of the
SDK's 16 KiB default. Entries exceeding that budget fail as whole records; no
shortened fragment is sent, and ordinary local logging/business outcomes remain
unchanged. Unrelated logger scopes retain their existing metadata-only alert
policy. Transport authorization is separate from application-selected fields.

The shared SDK sends asynchronously: a nil Send result confirms local queue
admission, not remote receipt. The existing receiver must separately confirm
GraphQL and provider completion. There is no added archive, storage API, retry,
listener or persistent mount. Synthetic startup and caller/SDK/nginx/receiver
tests cover complete chosen content, context snapshots, single delivery and
whole-record rejection; they do not establish every future production delivery,
human reading, downstream retention or a downloaded attachment hash.
