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
or an HTTP status as the JSON-RPC outcome: legacy-session requests carry this
error in HTTP 200, while protocol 2026-07-28 requests carry it in HTTP 400.

## Acceptance criterion

One real MCP protocol failure produces exactly one sanitized notification whose
failure_id joins the complete local and central record of the same event.
Accepted enqueueing by the alert sender and Telegram delivery are separate
outcomes: the sender queue is in memory, Close cancels instead of flushing, and
enqueue acceptance does not prove external delivery.

## Scope: protocol errors only

This covers failures the MCP SDK reports to its OnError hook: protocol errors,
unknown tools/resources/prompts, unsupported capabilities, unparsable
parameters, handler errors returned as JSON-RPC errors, and server-side
notification delivery failures. A tool that returns a successful JSON-RPC result
with isError=true (for example web_search returning "search failed: <category>")
does not reach this hook. Those failures stay with their tool-specific logging,
diagnostic policy, call auditing and billing outcome; repeating them here would
duplicate alerts.

## Notification boundary

The existing mcp request failed event authors one immutable mcp_failure object.
Only that sealed object, at that exact logger name and message, crosses the MCP
notification boundary. Arbitrary error text, request/response content,
arguments, session IDs, tool names and inherited context are not forwarded.
Web search/fetch scopes keep their existing faithful export; other scopes and
scope lookalikes keep metadata-only behavior. There is still one source error
event and one existing alert enqueue path, with no new destination,
configuration or production dependency.

The logger name and message are an internal contract. Producers use the
library/log constants RootLoggerName, ServiceLoggerScope, MCPHooksLoggerScope
and MCPRequestFailedMessage instead of repeating literals, and
TestMCPFailureAlertProductionWiring sends a failing request through NewServer so
a rename cannot silently revert the alert to {}.

## Envelope contract

| Field | Meaning |
| --- | --- |
| failure_id | Fresh server-generated UUID, identical in the alert and the complete local/central event. |
| method | The method the SDK reported to OnError, otherwise unknown. |
| request_id | Integral JSON-RPC ID within ±(2^53−1), or a canonical non-nil UUID string. Other client IDs are omitted. |
| error_kind | Source classification from typed Go causes, otherwise request_failed. |
| error_code | JSON-RPC wire code the SDK returned, only within the reserved -32768..-32000 range. |

method covers every request method the pinned SDK dispatcher handles (including
tasks/get, tasks/list, tasks/result and tasks/cancel) and the SDK's notification
pseudo-method for server-side notification delivery failures. Values come from
the SDK's own MCPMethod constants. TestMCPFailureMethodsMatchSDK derives the same
set from the installed SDK source and fails when an upgrade adds, renames or
removes a method, or reports one from a new place.

request_id accepts int, int64, integral float64, json.Number and the SDK
RequestId wrapper holding one of those. A notification failure has no request
ID. When a client ID is omitted, use failure_id and the event timestamp instead.

error_kind and error_code answer different questions. error_kind says why the
source failed and is derived only from SDK sentinels and error types, never from
error text. error_code is what the client received on the wire. For example,
tool_not_found travels as -32602 and unsupported as -32601.

| error_kind | Typed cause |
| --- | --- |
| tool_not_found | server.ErrToolNotFound |
| resource_not_found | server.ErrResourceNotFound |
| prompt_not_found | server.ErrPromptNotFound |
| unsupported | server.ErrUnsupported: the method needs a capability this server does not advertise |
| unparsable_request | *server.UnparsableMessageError |
| notification_channel_blocked | server.ErrNotificationChannelBlocked; no wire code |
| canceled | context.Canceled |
| deadline_exceeded | context.DeadlineExceeded |
| protocol_error | Any other SDK protocol error carrying a JSON-RPC code |
| request_failed | No typed cause recognized |

A genuine unknown handler error still alerts as protocol_error with its actual
protocol code. It is not silently downgraded or replaced in the complete central
record. Expected unsupported resources/list, resources/templates/list,
prompts/list and prompts/get probes keep their existing debug classification
and send no alert.

Unknown-tool requests remain errors. docs/ref/ora_agent_readiness.md suggests
treating known synthetic unknown-tool probes as debug noise, but this service
does not adopt that yet. The tool name is client-controlled, so there is no
verifiable way to tell a readiness probe from real client misuse, and the
incident does not justify suppressing real client failures. The alert now
carries error_kind tool_not_found, which is enough to triage without the private
name. Classifying verified readiness probes separately is a follow-up if alert
volume warrants it.

## Correlation

Use the alert timestamp, service/host identity and failure_id to find the full
canonical event in central telemetry. The OTLP record carries mcp_failure as a
structured attribute next to the complete request, session and error fields.
Payload-bearing attributes can be inside otel.record.json, rather than
separately indexed fields. Query a bounded time window and extract selected
protocol/error metadata in memory; do not export whole request records or
credentials. Numeric request ID 3 alone is not globally unique and must not be
used as a cross-session identity.

## Tests

- TestMCPRequestFailureAlert: the real SDK hook, protocol response and synthetic
  alert HTTP receiver. On the original source it returns {}. It checks method,
  numeric ID, typed category and actual protocol code; that complete local
  fields are kept; that private canaries are excluded; and that expected probes
  stay quiet.
- TestMCPFailureAlertReportsSDKMethods and
  TestMCPFailureAlertReportsNotificationDelivery: real SDK failures for task,
  completion, resource-read, logging, unparsable and notification paths keep
  their method and typed category.
- TestMCPFailureAlertProductionWiring: production logger names through
  NewServer's HTTP handler.
- TestMCPFailureOTLPCorrelation: the alerted failure_id equals the failure_id
  of the complete OTLP record.
- TestMCPFailureMethodsMatchSDK: method allowlist parity with the installed SDK.
- Exactly-one-alert checks put a barrier message on the alert sender's FIFO
  queue and require it to be the next delivery. Closing the sender cancels
  instead of flushing, so a queued duplicate could otherwise be discarded.
  TestAlertBarrierDetectsQueuedDuplicate is the negative control.
- Typed-cause, forged-object, duplicate-envelope, namespace, scope-lookalike,
  unsafe-ID and arbitrary-marshaler controls run in the automatic fast gate.
  Startup coverage is extended, and the existing web search/fetch fidelity
  assertions are kept.

No production failure was replayed to test notification delivery. Source tests
use loopback fixtures only. Deployment and production notification acceptance
require separate authorization.
