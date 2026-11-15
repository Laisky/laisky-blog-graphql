# MCP request failure diagnostics

The alert at 2026-11-14T19:33:35.296665009Z was a tools/call request with numeric
ID 3 for __orank_nonexistent_tool_probe__. Central canonical telemetry joins
the incoming request, MCP hook error and outgoing HTTP 200 response carrying
JSON-RPC error -32602 (tool not found). The deliberately nonexistent name
indicates a probe; caller identity and wider intent were not established.
This is separate from FileIO availability, whose authenticated search, create-only
write and exact readback succeeded during investigation.

The empty notification was caused by the application alert core forwarding nil
fields for graphql.laisky-graphql.mcp_hooks. Central/local diagnostic fields were
present. Do not interpret an empty alert as absence of an error at the source,
or an HTTP 200 response as JSON-RPC success.

## Notification boundary

The existing mcp request failed event now authors one immutable mcp_failure
object. Only that sealed object, at that exact logger name and message, crosses
the MCP notification boundary. Arbitrary error text, request/response content,
arguments, session IDs, tool names and inherited context are not forwarded.
Web search/fetch scopes keep their existing faithful export; other scopes and
scope lookalikes keep metadata-only behavior. There is still one source error
event and one existing alert enqueue path, with no new destination, dependency
or configuration. Enqueue acceptance does not prove external delivery.

The object contains:

- failure_id: a fresh server-generated UUID also recorded locally/centrally.
- method: a known protocol method or unknown.
- request_id: an integral JSON-safe numeric ID or canonical nonzero UUID.
  Other client IDs are omitted; use failure_id and the event timestamp instead.
- error_kind: typed tool_not_found, resource_not_found, prompt_not_found,
  canceled, deadline_exceeded, protocol_error, or honest request_failed.
- error_code: the actual JSON-RPC code obtained from the protocol error
  interface, when available in the reserved protocol/server error range. No
  code is guessed from error text.

A genuine unknown handler error still alerts as protocol_error with its actual
protocol code. It is not silently downgraded or replaced in the complete central
record. Expected unsupported resource/prompt probes retain their existing debug
classification and send no error alert. Unknown-tool requests remain errors;
the incident does not justify globally suppressing real client failures.

## Correlation and acceptance

Use the alert timestamp, service/host identity and failure_id to find the full
canonical event in central telemetry. Payload-bearing attributes can be inside
otel.record.json, rather than separately indexed fields. Query a bounded time
window and extract selected protocol/error metadata in memory; do not export
whole request records or credentials. Numeric request ID 3 alone is not globally
unique and must not be used as a cross-session identity.

TestMCPRequestFailureAlert exercises the real dependency hook, protocol response
and synthetic alert HTTP receiver. Its original-source RED returns {}; the fix
provides method, numeric ID, typed category and actual protocol code, preserves
complete local fields, excludes private canaries and keeps expected probes quiet.
Typed-cause, forged-object, duplicate-envelope, namespace, scope-lookalike,
unsafe-ID and arbitrary-marshaler controls run in the automatic fast gate.
Startup coverage is extended; existing web search/fetch fidelity assertions are retained.

No production failure was replayed to test notification delivery. Source tests
use loopback fixtures only. Deployment and production notification acceptance
require separate authorization.
