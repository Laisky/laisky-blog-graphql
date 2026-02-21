# Memory MCP Tools User Guide (Client Agent)

## Who this guide is for

This document is for client-agent developers who call MCP tools and want memory across multi-turn sessions.

It explains:

1. How to call memory tools in a normal turn lifecycle
2. How to handle retries and errors safely
3. How to inspect and maintain memory state when needed

It does not cover server deployment or server-side tuning.

## Tool list

Use these tools:

1. memory_before_turn
2. memory_after_turn
3. memory_run_maintenance
4. memory_list_dir_with_abstract

## Core identifiers

Your client must keep these values stable:

1. project: tenant or workspace scope
2. session_id: one conversation/session
3. turn_id: one model turn (must be unique per turn)
4. user_id: optional user identity label

Recommended turn_id format: a monotonic UUID-like value.

## Standard turn lifecycle

For every user turn:

1. Build current_input from the new user message (Responses-style items).
2. Call memory_before_turn.
3. Send returned input_items to your model.
4. Call memory_after_turn with the same project, session_id, and turn_id.

This is the only required pattern for normal usage.

## Call examples

### 1) memory_before_turn

Request:

```json
{
	"project": "demo",
	"session_id": "session-001",
	"user_id": "user-001",
	"turn_id": "turn-20260221-0001",
	"current_input": [
		{
			"type": "message",
			"role": "user",
			"content": [
				{
					"type": "input_text",
					"text": "Please summarize today plan in 3 bullets"
				}
			]
		}
	],
	"max_input_tok": 120000
}
```

Response:

```json
{
	"input_items": [
		{
			"type": "message",
			"role": "developer",
			"content": [
				{
					"type": "input_text",
					"text": "Memory recall: ..."
				}
			]
		},
		{
			"type": "message",
			"role": "user",
			"content": [
				{
					"type": "input_text",
					"text": "Please summarize today plan in 3 bullets"
				}
			]
		}
	],
	"recall_fact_ids": ["fact_xxx"],
	"context_token_count": 1830
}
```

### 2) memory_after_turn

Request:

```json
{
	"project": "demo",
	"session_id": "session-001",
	"user_id": "user-001",
	"turn_id": "turn-20260221-0001",
	"input_items": ["...same items used for model input..."],
	"output_items": [
		{
			"type": "message",
			"role": "assistant",
			"content": [
				{
					"type": "output_text",
					"text": "- Item A\n- Item B\n- Item C"
				}
			]
		}
	]
}
```

Response:

```json
{
	"ok": true
}
```

### 3) memory_run_maintenance (optional)

Request:

```json
{
	"project": "demo",
	"session_id": "session-001"
}
```

Response:

```json
{
	"ok": true
}
```

### 4) memory_list_dir_with_abstract (optional)

Request:

```json
{
	"project": "demo",
	"session_id": "session-001",
	"path": "",
	"depth": 8,
	"limit": 200
}
```

Response:

```json
{
	"summaries": [
		{
			"path": "/memory/session-001/meta",
			"abstract": "...",
			"updated_at": "2026-02-21T16:00:00Z",
			"has_overview": true
		}
	]
}
```

## Retry and idempotency guidance

1. If memory_after_turn times out on network, retry with the same turn_id.
2. Do not generate a new turn_id for retries of the same logical turn.
3. If you receive a retryable busy error, backoff and retry.

Suggested backoff:

1. 200ms
2. 500ms
3. 1s
4. 2s

## Error handling

Memory tools return structured error payloads with:

1. code
2. message
3. retryable

Typical codes:

1. INVALID_ARGUMENT: request fields are missing or invalid
2. PERMISSION_DENIED: auth missing or not allowed
3. RESOURCE_BUSY: concurrent operation lock conflict
4. INTERNAL_ERROR: transient or unexpected server issue

Client strategy:

1. retry only when retryable is true
2. never retry INVALID_ARGUMENT without fixing payload
3. preserve project, session_id, and turn_id across retries

## Practical best practices

1. Keep one active writer flow per session when possible.
2. Call memory_after_turn immediately after model output is produced.
3. Run memory_run_maintenance periodically for long-lived sessions.
4. Use memory_list_dir_with_abstract for debugging memory quality.
5. Keep content format aligned with Responses-style items.

## Minimal pseudo workflow

```text
on user message:
	turn_id = new_unique_turn_id()
	before = call memory_before_turn(project, session_id, turn_id, current_input)
	output_items = run_model(before.input_items)
	call memory_after_turn(project, session_id, turn_id, before.input_items, output_items)
	return output_items
```

## Troubleshooting checklist

1. If recall seems empty, verify you are calling memory_after_turn every turn.
2. If writes fail with busy errors, reduce concurrent commits in the same session.
3. If memory quality drops over long sessions, run memory_run_maintenance.
4. If session structure looks odd, inspect with memory_list_dir_with_abstract.
