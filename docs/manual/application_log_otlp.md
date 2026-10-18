# Private application logs through go-fluentd

The GraphQL API optionally sends a privacy-restricted log stream to the existing
authenticated OTLP/HTTP collector. Stdout and existing alerts remain unchanged.
This is one export branch; do not also collect the same stdout stream into the
central log store.

Set these only in the deployment's private configuration, never in Git:

| Variable | Meaning |
| --- | --- |
| APP_LOG_OTLP_ENDPOINT | HTTPS collector URL, optionally ending in /v1/logs; absent disables export |
| APP_LOG_OTLP_TOKEN_FILE | Read-only file containing the existing bearer token; maximum 4096 bytes |
| APP_LOG_OTLP_CA_FILE | Read-only PEM CA file; maximum 1 MiB |
| APP_LOG_OTLP_HOST | Fixed operational host label, for example b1 |
| APP_LOG_OTLP_ENV | Fixed environment label, for example production |

The resource always identifies service.name=graphql and service.namespace=laisky.
Host/environment values accept only ASCII letters, digits, dots, underscores and
hyphens. The exporter rejects endpoint user information, queries, fragments,
non-log paths, plaintext HTTP in production and HTTP redirects. It verifies TLS
against the configured CA without skipping hostname verification. It does not
use a proxy. Standard OTEL exporter/resource environment settings do not override
the private provider's explicit configuration. No global OTel provider is changed.
The SDK merges OTEL_RESOURCE_ATTRIBUTES even with an explicit resource. When this
optional exporter is enabled, inherited resource settings must therefore contain
only the four fixed identity keys shown above, which the explicit resource
overrides. Unknown keys, malformed pairs/percent escapes and input above 16 KiB
fail configuration with a fixed error before SDK parsing. OTEL_SERVICE_NAME is
overridden by service.name=graphql. Disabled export does not inspect these inputs.
Configuration is process-scoped and idempotent; changes require a process restart.

The init-time hook is inherited by child loggers captured before API startup.
The API configures its private provider before running application initialization. Maintenance commands retain their existing behavior.
Entries already admitted by the local logger are exported at their original
severity, including DEBUG when deliberately enabled. The default console remains INFO.
Changing the local logger level still gates export; the hook cannot enable a
previously filtered entry.

The remote body preserves the original operational message after targeted
credential, URL and labeled-payload sanitization using the shared go-utils
SanitizeDiagnosticText contract. Ordinary future string, bool, signed/unsigned
integer, float and duration fields survive without an event or field allowlist.
Error fields retain sanitized Error() text without stacks or verbose formatting;
panicking error methods produce a fixed marker. Error methods must return promptly.
Unsigned values above the OTLP signed integer range become decimal strings.
Duration fields preserve Zap's standard numeric seconds; explicitly authored numeric
duration_ms fields keep milliseconds. Inline time values use UTC RFC3339Nano.

Targeted normalized credential and private-payload key handling excludes
authentication headers, cookies, tokens, passwords, raw requests/responses,
body/payload fields and the actual memory/file content keys used by this app.
Counters such as token_count and booleans such as password_reset_required remain.
The configured OTLP bearer is also removed if echoed by an application message.
SDK URL sanitization replaces complete URLs, protecting user information, query
credentials and private paths while preserving surrounding failure context.

Objects, arrays, reflected values, Stringers, bytes and unsupported Zap types are
excluded before arbitrary encoder callbacks run. A namespace stops subsequent
fields to preserve its scoping semantics. Duplicate names keep the first accepted
value. Fixed resource identity keys, logger.name, bridge accounting keys and
VictoriaLogs canonical record keys cannot be spoofed by application fields.
These exclusions increment app.log.attributes_dropped rather than silently
claiming complete structured-object forwarding.

Records have a 4 KiB body, 2 KiB strings, 128-byte keys, at most 24 attributes
including two reserved accounting slots, and a 16 KiB approximate content budget
including per-field overhead, with at most 256 input fields examined. Oversized projected strings carry a truncation
marker and app.log.content_truncated=true. Names/values are bounded before SDK
queue admission. This is targeted sanitization, not a guarantee that arbitrary
unlabelled personal prose is safe. Existing source-level request/content
redaction remains necessary; the bridge never reads HTTP bodies itself.
Stdout preserves the application's original fields and messages.

The official SDK batches asynchronously: 512 pending records, batches of 128,
one-second partial flush and a five-second export/retry deadline. There is no
custom unbounded queue. A blocked collector does not block normal logging on
network I/O. Overflow drops records according to the SDK queue policy. The
application queue is volatile: abrupt process loss before collector
acknowledgement can lose records. A timeout after receiver acceptance may cause
duplicates on retry. Stdout remains local evidence; go-fluentd durability begins
only at its accepted-response boundary.

SnapshotOTLP exposes process-local Emitted, ExportedRecords, FailedExportRecords
and Unresolved. ExportedRecords counts a successful SDK Export call, not a
VictoriaLogs insert or a durable end-to-end acknowledgement. FailedExportRecords
counts the records in calls returning an error, after the configured retries.
Unresolved includes queued, in-flight and overflow-lost records during runtime;
it is not an exact live queue size or drop counter. After a successful shutdown
drain, remaining Unresolved identifies records the SDK did not export or fail.
The SDK's optional global metric instrumentation is left unchanged; this feature
does not install a global meter provider or claim independent drop alerting.

Deploy with a container stop grace period of at least 30 seconds, as provided
by the coordinated VPS Compose deployment change. Docker's default ten-second
stop window can SIGKILL the process before both bounded drains complete.

SIGTERM/interrupt stop HTTP admission, allow up to ten seconds for accepted HTTP
requests, then give the provider a fresh ten-second shutdown deadline. A failed
HTTP drain closes remaining connections. Errors returned by the exporter and shutdown are replaced with fixed diagnostics.
This adapter does not replace the SDK's global diagnostic or metric handlers.
SDK-internal instrumentation and partial-success handling remain SDK behavior;
receiver-controlled response diagnostics are not covered by a universal privacy
claim. Application-owned returned errors contain no request body, bearer token,
receiver response text or configuration value.

## Validation

Run named tests with the shared dev validation lease:

```sh
go test -race -count=1 -timeout 90s ./library/log -run TestOTLP
go test -race -count=1 -timeout 90s ./internal/web -run TestServeHTTP
go test -race -count=1 -timeout 90s ./cmd -run 'TestAPIOptionalLogging|TestAPIRejectsUnsafeLogging|TestStartupAlertDiagnosticScope'
```

The external test is deliberately opt-in. Supply disposable authenticated HTTPS
receiver configuration through the private variables above, then run:

```sh
OTLP_LOG_ACCEPTANCE=1 go test -count=1 ./library/log -run '^TestOTLPExternalAcceptance$'
```

It emits one synthetic entry through the production init hook/configuration,
flushes and checks SDK success. The caller must separately verify exactly one
VictoriaLogs row with service.name=graphql, the chosen host/environment,
the exact body synthetic operational refresh completed, stage=cache,
future_revision=parser-v2, channel_count=17, duration_ms=12.5, cache_ready=true,
status_code=299 and success=true, and absence of the synthetic secret canaries.
That isolated evidence does not replace actual deployed GraphQL acceptance or
prove durable recovery by itself. Production credential mounts, service
recreation and release require their separately authorized delivery steps.
