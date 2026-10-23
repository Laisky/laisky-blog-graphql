# Application OTLP logging references (2026-10-18)

The implementation follows the official Go log SDK and OTLP HTTP exporter,
verified against current documentation and the locally available v0.22.0 modules
aligned with this repository's existing OTel v1.46.0 dependencies.

- https://pkg.go.dev/go.opentelemetry.io/otel/sdk/log :
  providers own log processing and lifecycle; batch processors expose bounded
  pending-record queues, export batch size, interval and timeout.
- https://pkg.go.dev/go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp :
  official HTTP/protobuf transport supports explicit endpoint, headers, TLS,
  timeout and retry configuration.
- https://opentelemetry.io/docs/languages/sdk-configuration/otlp-exporter/ :
  generic endpoints append signal paths while signal-specific URLs use their
  configured paths. This application's explicit endpoint normalization permits
  only /v1/logs, avoiding accidental path ambiguity.
- https://pkg.go.dev/go.opentelemetry.io/contrib/bridges/otelzap :
  the official bridge targets go.uber.org/zap; this repository uses the distinct
  github.com/Laisky/zap fork.

The fork's HooksWithFields callback orders callsite fields before raw bound
fields and re-evaluates bound object callbacks. A small private Core adapter
reuses its correct accepted-entry gate while keeping immutable native With
snapshots in source order. It does not introduce a second source Check/Write,
custom queue or dependency fork. The optional branch is prepared only when
opted in before logger creation; disabled source callbacks are unchanged.
Earlier redaction/key-exclusion/truncation and JSON-projection candidates are
superseded. Their receipts are historical, not acceptance of this native revision.

Official data-model references checked on 2026-10-18:
- https://opentelemetry.io/docs/specs/otel/logs/data-model/ defines meaningful
  translation of source logs and arbitrary application attributes.
- https://opentelemetry.io/docs/specs/otel/common/ describes AnyValue maps,
  arrays, primitives and empty values.

- https://opentelemetry.io/docs/specs/otel/common/attribute-type-mapping/ :
  invalid Unicode strings map to Bytes; unsupported numeric ranges/precision map
  to exact decimal strings; duplicate-key maps use unique keys with value arrays.

The cached v1.46.0 attribute API and v0.22.0 HTTP exporter already support native
maps, slices and bytes; no dependency upgrade is needed. Ordered ObjectEncoder
and ArrayEncoder implementations preserve native typed values and source branch
snapshots. Reflection alone follows JSON marshal semantics and token parsing.
Native durations retain signed nanoseconds and time uses RFC3339Nano. Negative
count/value-length options disable SDK truncation. Duplicate keys use the official
multimap mapping instead of SDK duplicate-key passthrough. Whole-record protocol
failures are visible as Rejected and fixed diagnostics, without partial rewriting.
Actual storage fidelity remains a separate gate for null/empty values, metadata,
nonfinite floats, reserved names and resource/record collisions.

No new SDK global provider, meter provider, process-wide error handler or custom
queue is installed. The v0.22.0 SDK has no public BatchProcessor option for a
private meter provider. Its queue overflow is therefore reported honestly through
post-drain accounting rather than labelled as an exact runtime drop counter.

## Flat-store mapping qualification

Source/SDK fidelity does not imply that every backend retains scope, null values,
empty maps or duplicate/dotted keys. Verify those cases through actual storage
before releasing this bridge. Official VictoriaLogs references:

- https://docs.victoriametrics.com/victorialogs/data-ingestion/opentelemetry/ :
  resource labels become stream fields; a stream-field override does not itself
  preserve colliding resource/record keys.
- https://docs.victoriametrics.com/victorialogs/data-ingestion/ :
  VL-Preserve-JSON-Keys can preserve selected JSON values instead of flattening;
  applicability to a pinned native OTLP parser requires actual source/testing.
- https://opentelemetry.io/docs/specs/semconv/registry/attributes/log/ :
  log.record.original represents an original string record when Body differs.
  It is a candidate preservation mechanism, not permission to silently drop
  fields or a claim that name collisions are solved.
- https://github.com/open-telemetry/opentelemetry-collector-contrib/tree/main/exporter/elasticsearchexporter :
  pinned v0.161 none-mode source flattens/deduplicates and drops null. Bodymap
  retains a chosen Body map but omits other metadata and has document-level
  failure acknowledgement limitations. These modes did not qualify as a
  faithful durable replacement. A native OTLP storage representation requires
  independent actual receiver, query and replay qualification.

Actual isolated pinned VictoriaLogs v1.52 qualification found native resource/log
name collisions can panic its parser. A candidate gateway map/array snapshot kept
ordinary JSON-shaped content, but nonfinite floats produced invalid stored JSON
and scalar type identity was lost. It did not qualify as a general type-faithful
storage codec. Source/SDK tests and an ordinary JSON-subset delivery campaign do
not resolve that blocked release gate. No live gateway or production image was
changed by those experiments.
