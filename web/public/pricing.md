# Shared-tool pricing

MCP and GraphQL are peer interfaces over the same underlying functions. Disabling
an MCP tool does not change the GraphQL price or disable the GraphQL operation.
The browser does not add a second charge to either interface.

## Current configured prices

The `pricing` field of [runtime-config.json](./runtime-config.json) publishes the
per-call prices from the exact billing variables used by the running server.
Each item has `currency: "USD"`, `unit: "call"` and a decimal-string `amount`.
The homepage uses this metadata for search, fetch and extraction; unavailable or
invalid metadata is displayed as **Price unavailable**, not assumed to be free.

The metered functions are `web_search`, `web_fetch`, `extract_key_info` and
`find_tool`. In particular, extraction is not an unconditionally free operation.
This document deliberately does not maintain a second hard-coded price table.

## Unmetered interfaces and resource costs

The Inspector and call-log viewer are console interfaces, not paid tool calls.
The current FileIO, memory and human-coordination wrappers have no positive
per-call wrapper price. Model/indexing resources can have separate operational
costs; a zero wrapper charge is not a promise that all underlying resources are
free.

Pricing metadata is a snapshot of the server's configured tariff. It is not a
payment receipt, authorization decision, reservation or guarantee about a future
deployment. The external account enforces billing. A provider failure does not
by itself prove that a prior consume was refunded, and a timeout may have an
unknown outcome. Do not automatically replay a charge or infer a zero bill from
an error. The remaining reconciliation work is tracked in PR #49.
