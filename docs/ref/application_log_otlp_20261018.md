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

The fork's existing HooksWithFields API already carries call-site and bound
fields into a callback. An init-time callback with an atomic private emitter
avoids replacing stdout or introducing another fork-specific Core implementation.
It remains inactive until the API configures its private SDK. A broad bounded inline-field projection reuses the SDK's targeted diagnostic
text sanitization and credential/payload key contract. It preserves operational
messages and unknown future scalar fields rather than substituting a generic
body. Unsupported executable object/array/reflection/Stringer encoders are not
invoked. Error() text is sanitized with a panic guard; source-level private
content handling is still required because unlabeled prose is not detectable.

No new SDK global provider, meter provider, process-wide error handler or custom
queue is installed. The v0.22.0 SDK has no public BatchProcessor option for a
private meter provider. Its queue overflow is therefore reported honestly through
post-drain accounting rather than labelled as an exact runtime drop counter.
