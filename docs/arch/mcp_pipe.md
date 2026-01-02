# MCP `mcp_pipe` Tool

## Menu

- [MCP `mcp_pipe` Tool](#mcp-mcp_pipe-tool)
    - [Menu](#menu)
    - [User Story](#user-story)
    - [Goals](#goals)
    - [Non-goals](#non-goals)
    - [Tool Contract](#tool-contract)
        - [Pipeline Spec Schema (conceptual)](#pipeline-spec-schema-conceptual)
        - [Step Types](#step-types)
        - [Referencing Outputs](#referencing-outputs)
    - [Execution Semantics](#execution-semantics)
        - [Sequential Execution](#sequential-execution)
        - [Parallel Execution](#parallel-execution)
        - [Nested Pipelines](#nested-pipelines)
    - [Output Format](#output-format)
    - [Billing and Auditing](#billing-and-auditing)
    - [Safety Limits](#safety-limits)
    - [Configuration](#configuration)
    - [Implementation Notes](#implementation-notes)
        - [Code Locations](#code-locations)
        - [Supported Tools](#supported-tools)
    - [Examples](#examples)
        - [Search → Fetch → Extract](#search--fetch--extract)
        - [Parallel Fetch](#parallel-fetch)

## User Story

As a user of the MCP endpoint, I want to execute a multi-step information pipeline (e.g., search → fetch → extract) with a single tool call, so that I can reduce back-and-forth tool invocations, simplify agent logic, and reliably chain outputs from one tool into the next.

Acceptance criteria:

- A single call can run multiple existing MCP tools in a defined order.
- Outputs from earlier steps can be used as inputs to later steps.
- The pipeline supports:
    - Sequential execution
    - Parallel execution for independent steps
    - Nested pipelines
- The tool returns a structured, machine-readable result including per-step outputs and errors.

## Goals

- Provide a lightweight “orchestrator” tool that composes existing MCP tools.
- Keep the contract JSON-first and easy to generate programmatically.
- Preserve existing tool behavior (auth, billing, logging) by invoking existing handlers.

## Non-goals

- Building a fully-featured scripting language / DSL.
- Adding advanced control flow (loops, conditionals, retries) beyond the basics.
- Introducing new search/fetch/extraction capabilities; `mcp_pipe` only composes.

## Tool Contract

**Name**: `mcp_pipe`

**Description**: Execute a pipeline of MCP tools (sequential, parallel, nested) with output passing.

**Input**: The tool accepts either:

1. A `spec` field containing a JSON object or JSON-encoded string, or
2. The pipeline spec object as the top-level arguments object.

### Pipeline Spec Schema (conceptual)

```json
{
    "vars": { "q": "..." },
    "steps": [
        { "id": "search", "tool": "web_search", "args": { "query": "${vars.q}" } },
        {
            "id": "fetch",
            "tool": "web_fetch",
            "args": { "url": { "$ref": "steps.search.structured.results.0.url" } }
        },
        {
            "id": "extract",
            "tool": "extract_key_info",
            "args": {
                "query": "${vars.q}",
                "materials": "${steps.fetch.structured.content}"
            }
        }
    ],
    "return": { "$ref": "steps.extract.structured.contexts" },
    "continue_on_error": false
}
```

### Step Types

Each step must specify **exactly one** of the following:

- **Tool step**: `{ "id": "...", "tool": "<tool_name>", "args": { ... } }`
- **Parallel group**: `{ "id": "...", "parallel": [ <step>, <step>, ... ] }`
- **Nested pipeline**: `{ "id": "...", "pipe": <spec> }`

Step IDs must be non-empty and unique within the same `steps` list.

### Referencing Outputs

`mcp_pipe` supports two mechanisms:

1. **String interpolation**: In any string field, occurrences of `${...}` are replaced using values from the environment.
2. **Typed reference**: Any object of the form `{ "$ref": "path.to.value" }` is replaced by the referenced value (preserving its type).

Available root environment keys:

- `vars`: from `spec.vars`
- `steps`: per-step results written during execution
- `last`: the last step result object

Notes on paths:

- Paths are dot-delimited.
- Numeric segments index arrays, e.g. `steps.search.structured.results.0.url`.
- For parallel groups, child results are accessible under:
    - `steps.<parallel_step_id>.children.<child_id>...`

## Execution Semantics

### Sequential Execution

Steps in `spec.steps` are executed in order. Each step result is stored in `steps[step.id]`.

### Parallel Execution

A parallel group executes its children concurrently, bounded by a server-side concurrency limit.

The group result stores:

- `children`: map from child ID → child result object
- `ok`/`error`: the group-level status (if any child fails, the group is marked failed)

### Nested Pipelines

A nested pipeline step executes an embedded `pipe` spec.

The nested step result stores:

- `result`: the nested pipeline’s selected return value
- `steps`: the nested pipeline’s per-step results

## Output Format

`mcp_pipe` returns structured JSON with:

```json
{
    "ok": true,
    "error": "",
    "result": "<any>",
    "steps": {
        "<id>": {
            "id": "...",
            "kind": "tool|parallel|pipe",
            "ok": true,
            "error": "",
            "structured": { "...": "..." },
            "text": "...",
            "children": { "...": "..." },
            "result": "<any>",
            "steps": { "...": "..." }
        }
    }
}
```

If any step fails:

- When `continue_on_error=false` (default), execution stops at the first failure.
- When `continue_on_error=true`, execution continues, but the final `ok` is `false` and `error` is populated.

## Billing and Auditing

- `mcp_pipe` itself does not introduce new billing costs; sub-tools perform their existing billing checks.
- Billing is applied per sub-tool call using the caller's Authorization context. If the overall pipeline fails, any sub-tool steps that completed successfully are still billed (there is no rollback).
- Each sub-tool invocation is executed through the existing MCP server handlers, so existing auditing (`recordToolInvocation`) applies per step.
- `mcp_pipe` is also audited as a tool call with `baseCost=0`.

## Safety Limits

To avoid abuse and runaway pipelines, the implementation enforces limits:

- Maximum total steps per call (default: 50)
- Maximum nesting depth (default: 5)
- Maximum parallel concurrency (default: 8)

These limits are currently implemented server-side in code defaults.

## Configuration

Enable/disable `mcp_pipe` via:

- `settings.mcp.tools.mcp_pipe.enabled` (default: `true`)

## Implementation Notes

### Code Locations

- Tool implementation: [internal/mcp/tools/mcp_pipe.go](internal/mcp/tools/mcp_pipe.go)
- Server wiring/registration: [internal/mcp/server.go](internal/mcp/server.go)
- Tool enable flag: [internal/mcp/settings.go](internal/mcp/settings.go)
- Unit tests: [internal/mcp/tools/mcp_pipe_test.go](internal/mcp/tools/mcp_pipe_test.go)

### Supported Tools

The current invoker supports composing these tools:

- `web_search`
- `web_fetch`
- `extract_key_info`
- `ask_user`
- `get_user_request`

Direct recursion (`mcp_pipe` calling `mcp_pipe`) is rejected; nested pipelines should use the `pipe` step type.

## Examples

### Search → Fetch → Extract

```json
{
    "vars": { "q": "mcp protocol overview" },
    "steps": [
        { "id": "search", "tool": "web_search", "args": { "query": "${vars.q}" } },
        {
            "id": "fetch",
            "tool": "web_fetch",
            "args": { "url": { "$ref": "steps.search.structured.results.0.url" } }
        },
        {
            "id": "extract",
            "tool": "extract_key_info",
            "args": {
                "query": "${vars.q}",
                "materials": "${steps.fetch.structured.content}"
            }
        }
    ],
    "return": { "$ref": "steps.extract.structured.contexts" }
}
```

### Parallel Fetch

```json
{
    "vars": { "q": "golang", "u1": "https://a", "u2": "https://b" },
    "steps": [
        {
            "id": "pages",
            "parallel": [
                { "id": "p1", "tool": "web_fetch", "args": { "url": "${vars.u1}" } },
                { "id": "p2", "tool": "web_fetch", "args": { "url": "${vars.u2}" } }
            ]
        },
        {
            "id": "extract",
            "tool": "extract_key_info",
            "args": {
                "query": "${vars.q}",
                "materials": "${steps.pages.children.p1.structured.content}\n${steps.pages.children.p2.structured.content}"
            }
        }
    ],
    "return": { "$ref": "steps.extract.structured.contexts" }
}
```
