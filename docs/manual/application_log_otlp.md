# Private application logs through go-fluentd

The GraphQL API optionally sends an application log stream to the existing
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
Host/environment values must be explicitly nonempty valid UTF-8 and otherwise
retain the operator-selected value, including Unicode, spaces and long labels. The exporter rejects endpoint user information, queries, fragments,
non-log paths, plaintext HTTP in production and HTTP redirects. It verifies TLS
against the configured CA without skipping hostname verification. It does not
use a proxy. Standard OTEL exporter settings do not override
the private provider's explicit transport configuration. No global OTel provider is changed.
The SDK merges application-chosen OTEL_RESOURCE_ATTRIBUTES into the resource.
Custom attributes are preserved, including unknown names. The four explicit
service/namespace/host/environment identities take precedence on collision;
OTEL_SERVICE_NAME cannot replace service.name=graphql. Malformed resource pairs,
invalid UTF-8 or invalid percent escapes fail configuration with a fixed error
before SDK diagnostics. No content-key allowlist or arbitrary resource-text cap
is applied. Disabled export does not inspect these inputs.
Configuration is process-scoped and idempotent; changes require a process restart.

The init-time private branch is prepared when APP_LOG_OTLP_ENDPOINT is present
before logger creation. Child loggers captured before provider startup inherit it.
Disabled export keeps the original core and evaluates no extra field callbacks.
Changing the environment after a logger was created cannot reconstruct earlier
dynamic fields; configure the environment before process startup and restart.
The adapter reuses the fork's accepted-entry gate, preserving sampling and
existing stdout, alert and fatal behavior without checking or writing twice.
The API configures its private provider before running application initialization. Maintenance commands retain their existing behavior.
Entries already admitted by the local logger are exported at their original
severity, including DEBUG when deliberately enabled. The default console remains INFO.
Changing the local logger level still gates export; the hook cannot enable a
previously filtered entry.

The remote Body is the original application message. The bridge adds no automatic
sanitizer, token-literal replacement, field allowlist/denylist, value masking,
field-count cap or text truncation. Application-selected log levels and existing
source logging/alert policies remain unchanged. Applications must choose what to
log; this transport does not independently collect HTTP request bodies.

An ordered native Zap ObjectEncoder/ArrayEncoder evaluates each field once for
this output branch. With-bound callbacks are captured once per output branch;
subsequent writes reuse immutable snapshots and retain namespace and field order.
Callsite callbacks run for each output branch and should return promptly. Original
Stringer/Error and reflected JSON tags/marshalers retain application semantics.
Arrays, objects, null and empty containers become native OTLP values. Binary and
invalid UTF-8 strings become exact Bytes values; valid ByteString remains text.
Duration is signed nanoseconds; time is RFC3339Nano with the original offset.
Native float types remain OTLP doubles, including negative zero, NaN and infinity.
Complex values use Go's full-precision complex string representation.

The official AnyValue mapping emits integers outside signed int64 and reflected
JSON decimals outside double range/shortest-roundtrip precision as exact decimal
strings. An object containing duplicate names becomes a unique-key multimap:
every key has an array of its source values, including singleton keys. Value
order is preserved. Reflected map iteration order is application JSON behavior.
The SDK's record attribute count and string-length limits are explicitly disabled,
overriding inherited defaults. There is no new per-record byte bound; large
records can consume more memory or be rejected by a receiver.

Original logger name, caller and stack metadata are added only when the application
has not already supplied those same keys. Authored resource-like or storage-like
field names remain attributes unchanged; resource identity and log attributes are
separate in OTLP. Backend flattening, duplicate-key and reserved-name behavior must
be qualified independently; the bridge does not claim universal storage fidelity.
Pinned native VictoriaLogs collision/type-loss regressions remain a release gate.
An ordinary JSON-subset campaign cannot establish general typed storage fidelity.

If encoding fails, a callback panics, a top-level attribute key is empty or a key has invalid UTF-8,
a reflected JSON string has a lone surrogate escape, or a value cannot be represented by OTLP, the entire remote record is rejected. Rejected
increments and a fixed error reaches the logger's configured error output. No
partial sanitized/truncated replacement is emitted. The original stdout branch
and its application-selected behavior remain intact.

The official SDK batches asynchronously: 512 pending records, batches of 128,
one-second partial flush and a five-second export/retry deadline. There is no
custom unbounded queue. A blocked collector does not block normal logging on
network I/O. Overflow drops records according to the SDK queue policy. The
application queue is volatile: abrupt process loss before collector
acknowledgement can lose records. A timeout after receiver acceptance may cause
duplicates on retry. Stdout remains local evidence; go-fluentd durability begins
only at its accepted-response boundary.

SnapshotOTLP exposes process-local Emitted, ExportedRecords, FailedExportRecords
and Unresolved, plus Rejected for whole-record projection failures. ExportedRecords counts a successful SDK Export call, not a
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
status_code=299 and success=true, and exact preservation of the synthetic API/auth/URL/request-body/nested canaries.
The common native fixture also contains Object/Array marshalers, null/empty
containers, application service.name/host.name/_msg fields and a namespace payload.
All fixture values are synthetic; never substitute actual secrets. Null, empty
container, duplicate-name and reserved-name fidelity need downstream assertions
in addition to this source/SDK test.
That isolated evidence does not replace actual deployed GraphQL acceptance or
prove durable recovery by itself. Production credential mounts, service
recreation and release require their separately authorized delivery steps.
