# MCP FileIO User Manual

This manual is for end users and integrators who want to call `file_stat`, `file_read`, `file_write`, `file_delete`, `file_rename`, `file_list`, and `file_search` through MCP.

## Menu

- [MCP FileIO User Manual](#mcp-fileio-user-manual)
  - [Menu](#menu)
  - [1. What FileIO is for](#1-what-fileio-is-for)
  - [2. Quick start (5 minutes)](#2-quick-start-5-minutes)
    - [2.1 Prepare environment variables](#21-prepare-environment-variables)
    - [2.2 Initialize MCP session (required)](#22-initialize-mcp-session-required)
    - [2.3 (Optional) list available tools](#23-optional-list-available-tools)
  - [3. Unified call format](#3-unified-call-format)
  - [4. Input rules (read this first)](#4-input-rules-read-this-first)
    - [4.1 `project`](#41-project)
    - [4.2 `path`](#42-path)
    - [4.3 Content and encoding](#43-content-and-encoding)
  - [5. Tool-by-tool guide with cURL examples](#5-tool-by-tool-guide-with-curl-examples)
    - [5.1 `file_write`](#51-file_write)
      - [Example A: create and write](#example-a-create-and-write)
      - [Example B: overwrite from byte offset](#example-b-overwrite-from-byte-offset)
    - [5.2 `file_read`](#52-file_read)
    - [5.3 `file_stat`](#53-file_stat)
    - [5.4 `file_list`](#54-file_list)
    - [5.5 `file_search`](#55-file_search)
    - [5.6 `file_delete`](#56-file_delete)
      - [Example A: delete one file](#example-a-delete-one-file)
      - [Example B: recursively delete a directory](#example-b-recursively-delete-a-directory)
    - [5.7 `file_rename`](#57-file_rename)
      - [Example A: rename one file](#example-a-rename-one-file)
      - [Example B: move a directory subtree](#example-b-move-a-directory-subtree)
      - [Example C: move a file into another project](#example-c-move-a-file-into-another-project)
  - [6. Common error codes](#6-common-error-codes)
  - [7. Minimal end-to-end flow](#7-minimal-end-to-end-flow)
  - [8. Integration best practices](#8-integration-best-practices)
  - [9. Shared defaults: the `@DEFAULT` project](#9-shared-defaults-the-default-project)
    - [9.1 How a project sees `@DEFAULT`](#91-how-a-project-sees-default)
    - [9.2 What changes are global](#92-what-changes-are-global)
    - [9.3 Recipes](#93-recipes)
    - [9.4 Search, history and isolation](#94-search-history-and-isolation)

## 1. What FileIO is for

MCP FileIO provides a remote, project-scoped workspace for text files. It is useful for AI agents and automation workflows that need durable shared files.

You can:

- Read and write files under a specific `project` (text content, `utf-8`).
- Browse directories (directories are logical/implicit).
- Delete a file or a directory subtree.
- Rename or move files and directory trees.
- Search indexed file content with `file_search`.

## 2. Quick start (5 minutes)

### 2.1 Prepare environment variables

```bash
export MCP_ENDPOINT="https://mcp.laisky.com"
export MCP_API_KEY="<YOUR_API_KEY>"
export MCP_AUTH="Authorization: Bearer ${MCP_API_KEY}"
```

### 2.2 Initialize MCP session (required)

MCP uses a session. Call `initialize` first, then read `Mcp-Session-Id` from response headers and include it in all following requests.

```bash
curl -i -sS "$MCP_ENDPOINT" \
	-H 'Content-Type: application/json' \
	-H "$MCP_AUTH" \
	--data-raw '{
		"jsonrpc":"2.0",
		"id":1,
		"method":"initialize",
		"params":{
			"protocolVersion":"2025-06-18",
			"capabilities":{},
			"clientInfo":{"name":"curl-client","version":"1.0.0"}
		}
	}'
```

Save the response header value:

```bash
export MCP_SESSION_ID="<SESSION_ID_FROM_RESPONSE_HEADER>"
```

### 2.3 (Optional) list available tools

```bash
curl -sS "$MCP_ENDPOINT" \
	-H 'Content-Type: application/json' \
	-H "$MCP_AUTH" \
	-H "Mcp-Session-Id: ${MCP_SESSION_ID}" \
	--data-raw '{
		"jsonrpc":"2.0",
		"id":2,
		"method":"tools/list",
		"params":{}
	}'
```

## 3. Unified call format

All FileIO calls use `tools/call`:

```json
{
  "jsonrpc": "2.0",
  "id": 100,
  "method": "tools/call",
  "params": {
    "name": "file_write",
    "arguments": {
      "project": "demo",
      "path": "/notes/todo.txt",
      "content": "hello"
    }
  }
}
```

Tool output is typically in `result.content[0].text` (as JSON string). Error payloads include:

```json
{ "code": "INVALID_PATH", "message": "...", "retryable": false }
```

## 4. Input rules (read this first)

### 4.1 `project`

- Required, length `1..128`
- Allowed characters: `a-z A-Z 0-9 _ - .`
- The reserved name `@DEFAULT` (exact case) is also accepted. It holds files
  shared by every project of your API key; see [section 9](#9-shared-defaults-the-default-project).
  Any other name containing `@` is rejected.

### 4.2 `path`

- Root path is `""`
- Non-root paths may be provided with or without a leading `/`; the tool adds
  the prefix automatically (for example, `docs/a.txt` becomes `/docs/a.txt`)
- Must not end with `/`
- Must not contain `//`, `.` or `..` segments
- Must not contain whitespace or control characters
- Max length is `512`

### 4.3 Content and encoding

- Text only
- `content_encoding` must be `utf-8` (default is `utf-8`)

## 5. Tool-by-tool guide with cURL examples

All examples below are directly runnable.

To reduce repetition, define a helper first:

```bash
mcp_call() {
	local payload="$1"
	curl -sS "$MCP_ENDPOINT" \
		-H 'Content-Type: application/json' \
		-H "$MCP_AUTH" \
		-H "Mcp-Session-Id: ${MCP_SESSION_ID}" \
		--data-raw "$payload"
}
```

### 5.1 `file_write`

Write or update a file.

- Required: `project`, `path`, `content`
- Optional: `content_encoding`, `offset`, `mode`
- `mode`:
  - `APPEND` (default): always writes at EOF
  - `OVERWRITE`: writes from `offset` without truncating tail bytes
  - `TRUNCATE`: clears file then writes (`offset` must be `0`)

#### Example A: create and write

```bash
mcp_call '{
	"jsonrpc":"2.0",
	"id":101,
	"method":"tools/call",
	"params":{
		"name":"file_write",
		"arguments":{
			"project":"demo",
			"path":"/docs/readme.txt",
			"content":"Hello FileIO\n",
			"mode":"APPEND"
		}
	}
}'
```

#### Example B: overwrite from byte offset

```bash
mcp_call '{
	"jsonrpc":"2.0",
	"id":102,
	"method":"tools/call",
	"params":{
		"name":"file_write",
		"arguments":{
			"project":"demo",
			"path":"/docs/readme.txt",
			"content":"MCP",
			"mode":"OVERWRITE",
			"offset":6
		}
	}
}'
```

Success response example:

```json
{ "bytes_written": 4 }
```

### 5.2 `file_read`

Read file content.

- Required: `project`, `path`
- Optional: `offset` (default `0`), `length` (default `-1`, read to EOF)

```bash
mcp_call '{
	"jsonrpc":"2.0",
	"id":103,
	"method":"tools/call",
	"params":{
		"name":"file_read",
		"arguments":{
			"project":"demo",
			"path":"/docs/readme.txt",
			"offset":0,
			"length":100
		}
	}
}'
```

Success response example:

```json
{ "content": "Hello MCPIO...", "content_encoding": "utf-8" }
```

### 5.3 `file_stat`

Check whether a path is a file, directory, or missing.

- Required: `project`
- Optional: `path` (empty or omitted means root)

```bash
mcp_call '{
	"jsonrpc":"2.0",
	"id":104,
	"method":"tools/call",
	"params":{
		"name":"file_stat",
		"arguments":{
			"project":"demo",
			"path":"/docs"
		}
	}
}'
```

Response example:

```json
{
  "exists": true,
  "type": "DIRECTORY",
  "size": 0,
  "created_at": "0001-01-01T00:00:00Z",
  "updated_at": "2026-02-13T08:00:00Z"
}
```

### 5.4 `file_list`

List files/directories under a path.

- Required: `project`
- Optional:
  - `path`: directory path; root is `""` (tool also accepts `"/"`)
  - `depth`: default `1`
    - `0`: only the `path` entry itself
    - `1`: immediate children
    - `>1`: recursive traversal
  - `limit`: max entries to return

```bash
mcp_call '{
	"jsonrpc":"2.0",
	"id":105,
	"method":"tools/call",
	"params":{
		"name":"file_list",
		"arguments":{
			"project":"demo",
			"path":"/docs",
			"depth":2,
			"limit":50
		}
	}
}'
```

Response example:

```json
{
  "entries": [
    { "name": "readme.txt", "path": "/docs/readme.txt", "type": "FILE", "size": 18, "created_at": "...", "updated_at": "..." },
    { "name": "sub", "path": "/docs/sub", "type": "DIRECTORY", "size": 0, "created_at": "0001-01-01T00:00:00Z", "updated_at": "..." }
  ],
  "has_more": false
}
```

### 5.5 `file_search`

Search indexed chunks inside a project.

- Required: `project`, `query`
  - Set `project` to `"*"` to search across every project owned by the caller. The wildcard is accepted only by `file_search`; all other file tools still require an explicit project.
- Optional:
  - `path_prefix`: prefix filter, for example `/docs`; a missing leading `/`
    is added automatically
  - `limit`: default `5`, max `20`

```bash
mcp_call '{
	"jsonrpc":"2.0",
	"id":106,
	"method":"tools/call",
	"params":{
		"name":"file_search",
		"arguments":{
			"project":"demo",
			"query":"readme",
			"path_prefix":"/docs",
			"limit":5
		}
	}
}'
```

Response example:

```json
{
  "chunks": [
    {
      "file_path": "/docs/readme.txt",
      "file_seek_start_bytes": 0,
      "file_seek_end_bytes": 120,
      "is_full_file": false,
      "chunk_content": "...",
      "file_summary": "This readme explains how to install and configure the demo project, lists its main commands, and points to the troubleshooting section.",
      "score": 0.93
    }
  ]
}
```

- `is_full_file=true` means this returned chunk byte range covers the whole file.
- `is_full_file=false` means this is only part of the file; call `file_read` if full content is needed.
- `file_summary` is a concise, English, file-level overview (at most 300 words / 2,048 bytes) describing the whole source file the chunk came from. Use it to understand a match without a second `file_read`. It is generated during indexing, so a just-written file may briefly return without it; a slow or unavailable summarizer yields a short deterministic fallback rather than an empty field. The summary never changes ranking.
- When `project="*"`, each chunk also includes a `project` field naming the source project. The field is omitted for single-project searches.

Cross-project example:

```bash
mcp_call '{
	"jsonrpc":"2.0",
	"id":106,
	"method":"tools/call",
	"params":{
		"name":"file_search",
		"arguments":{
			"project":"*",
			"query":"readme",
			"limit":5
		}
	}
}'
```

```json
{
  "chunks": [
    {
      "project": "demo",
      "file_path": "/docs/readme.txt",
      "file_seek_start_bytes": 0,
      "file_seek_end_bytes": 120,
      "is_full_file": false,
      "chunk_content": "...",
      "file_summary": "This readme explains how to install and configure the demo project and where to find troubleshooting steps.",
      "score": 0.93
    }
  ]
}
```

> Note: `file_search` can be eventually consistent. Recent writes may take a short time to appear.

### 5.6 `file_delete`

Delete a file or directory subtree.

- Required: `project`
- Optional:
  - `path`: target path
  - `recursive`: required for non-empty directory deletion

#### Example A: delete one file

```bash
mcp_call '{
	"jsonrpc":"2.0",
	"id":107,
	"method":"tools/call",
	"params":{
		"name":"file_delete",
		"arguments":{
			"project":"demo",
			"path":"/docs/readme.txt",
			"recursive":false
		}
	}
}'
```

#### Example B: recursively delete a directory

```bash
mcp_call '{
	"jsonrpc":"2.0",
	"id":108,
	"method":"tools/call",
	"params":{
		"name":"file_delete",
		"arguments":{
			"project":"demo",
			"path":"/docs",
			"recursive":true
		}
	}
}'
```

Success response example:

```json
{ "deleted_count": 3 }
```

> Root deletion is not allowed (`path=""` returns `PERMISSION_DENIED`).

### 5.7 `file_rename`

Rename or move a file/directory path, optionally into another project.

- Required: `project`, `from_path`, `to_path`, `expected_version` (the source file's version; see [FileIO versioning](fileio_versioning.md))
- Optional:
  - `overwrite`: default `false`; only applies to file rename target replacement
  - `target_project`: destination project for a cross-project move. Omit it or pass `""` to rename within `project`.
    `to_path`, the collision and overwrite rules, `expected_destination_version` and `destination_must_not_exist`
    all apply in `target_project`, and the moved bytes count against its storage quota. The file keeps its identity
    and version history; its revision advances, so re-read before editing it again. `"*"` is rejected.

When using PageIndex, a Markdown or PDF destination must keep its source format and have a current tree.
If indexing is still in progress, the move returns retryable `RESOURCE_BUSY` without changing either path.
Moving to an unsupported extension preserves the file but removes its PageIndex search entry.

#### Example A: rename one file

```bash
mcp_call '{
	"jsonrpc":"2.0",
	"id":109,
	"method":"tools/call",
	"params":{
		"name":"file_rename",
		"arguments":{
			"project":"demo",
			"from_path":"/docs/readme.txt",
			"to_path":"/docs/guide.txt",
			"overwrite":false
		}
	}
}'
```

#### Example B: move a directory subtree

```bash
mcp_call '{
	"jsonrpc":"2.0",
	"id":110,
	"method":"tools/call",
	"params":{
		"name":"file_rename",
		"arguments":{
			"project":"demo",
			"from_path":"/docs",
			"to_path":"/archive/docs",
			"overwrite":false
		}
	}
}'
```

#### Example C: move a file into another project

```bash
mcp_call '{
	"jsonrpc":"2.0",
	"id":111,
	"method":"tools/call",
	"params":{
		"name":"file_rename",
		"arguments":{
			"project":"demo",
			"from_path":"/docs/guide.txt",
			"target_project":"archive",
			"to_path":"/2026/guide.txt",
			"expected_version":"<version from file_read or file_stat>"
		}
	}
}'
```

Success response example:

```json
{ "moved_count": 3 }
```

## 6. Common error codes

- `INVALID_PATH`: invalid `project` or `path`
- `INVALID_OFFSET`: invalid offset/length/depth/mode position
- `NOT_FOUND`: target path does not exist
- `ALREADY_EXISTS`: destination path already exists in rename/move/write-conflict scenarios
- `IS_DIRECTORY`: file operation used on directory path
- `NOT_DIRECTORY`: a parent segment is an existing file
- `NOT_EMPTY`: directory is not empty and `recursive=false`
- `PERMISSION_DENIED`: missing permission or forbidden operation (for example root delete)
- `PAYLOAD_TOO_LARGE`: write payload exceeds limit
- `QUOTA_EXCEEDED`: project storage quota exceeded
- `RESOURCE_BUSY`: lock timeout under concurrent mutations; retry later
- `SEARCH_BACKEND_ERROR`: search backend unavailable or disabled

## 7. Minimal end-to-end flow

```bash
# 1) Write
mcp_call '{"jsonrpc":"2.0","id":201,"method":"tools/call","params":{"name":"file_write","arguments":{"project":"demo","path":"/quick/start.txt","content":"hello"}}}'

# 2) Read
mcp_call '{"jsonrpc":"2.0","id":202,"method":"tools/call","params":{"name":"file_read","arguments":{"project":"demo","path":"/quick/start.txt"}}}'

# 3) List
mcp_call '{"jsonrpc":"2.0","id":203,"method":"tools/call","params":{"name":"file_list","arguments":{"project":"demo","path":"/quick","depth":1,"limit":20}}}'

# 4) Search
mcp_call '{"jsonrpc":"2.0","id":204,"method":"tools/call","params":{"name":"file_rename","arguments":{"project":"demo","from_path":"/quick/start.txt","to_path":"/quick/start-renamed.txt","overwrite":false}}}'

# 5) Delete
mcp_call '{"jsonrpc":"2.0","id":205,"method":"tools/call","params":{"name":"file_search","arguments":{"project":"demo","query":"hello","limit":5}}}'

# 6) Delete
mcp_call '{"jsonrpc":"2.0","id":206,"method":"tools/call","params":{"name":"file_delete","arguments":{"project":"demo","path":"/quick/start-renamed.txt","recursive":false}}}'
```

## 8. Integration best practices

- Always initialize a session and cache `Mcp-Session-Id`.
- Parse error JSON by `code` and branch your retry/fallback logic.
- For write retries, use an idempotent pattern (for example `file_stat` + conditional write).
- For `file_search`, allow a short delay after writes for indexing visibility.

## 9. Shared defaults: the `@DEFAULT` project

Every API key has a built-in project named `@DEFAULT`. Files you put there show
up in **all** of your other projects, like the lower layer of an overlay
filesystem. Use it for context every agent should share, such as user
preferences or coding conventions, instead of copying the same file into every
project. Design and exact rules: [`docs/proposals/fileio_default_project.md`](../proposals/fileio_default_project.md).

### 9.1 How a project sees `@DEFAULT`

- If the project has its own file at a path, you see the project's file.
- Otherwise, if `@DEFAULT` has a file at that path, you see the default.
- Names win as a whole: a project file `/notes` hides a default directory
  `/notes/...`, and any project file below `/notes/` hides a default file `/notes`.

Inside a project you never need to know where a file comes from. `file_read`,
`file_stat`, `file_list`, `file_search` and the history tools treat inherited files
exactly like the project's own files. They return no extra field, and `file_list`
shows each name once.

`@DEFAULT` itself is an ordinary project. Addressing it shows only its own files.

### 9.2 What changes are global

| You do this in a project | Result |
| --- | --- |
| Edit (any write mode) a file that comes from `@DEFAULT` | The default changes for **every** project. |
| Create a new file | The file is created in that project only. |
| Delete a file that comes from `@DEFAULT` | The default is deleted for every project that does not override it. Its history is kept. |
| Delete the project's own override of a default | The default becomes visible again. |
| Rename a file that comes from `@DEFAULT` | It is renamed for every project. |
| Rename a project file onto a default's path with `overwrite=true` | The project file overrides the default in that project only. |

All the usual version rules apply. A version from `file_read`/`file_stat` names
the exact file you saw, so an edit fails with `VERSION_CONFLICT` if another
project changed the default in the meantime.

### 9.3 Recipes

Seed a default (once, from anywhere):

```bash
mcp_call '{"jsonrpc":"2.0","id":301,"method":"tools/call","params":{"name":"file_write","arguments":{
  "project":"@DEFAULT","path":"/users.md","content":"Prefers concise English answers.\n","create_only":true}}}'
```

Every project now reads it:

```bash
mcp_call '{"jsonrpc":"2.0","id":302,"method":"tools/call","params":{"name":"file_read","arguments":{
  "project":"graphql","path":"/users.md"}}}'
# {"content":"Prefers concise English answers.\n","content_encoding":"utf-8","version":"<V_DEFAULT>"}
```

Editing it through a project edits the shared default:

```bash
mcp_call '{"jsonrpc":"2.0","id":303,"method":"tools/call","params":{"name":"file_write","arguments":{
  "project":"graphql","path":"/users.md","mode":"APPEND","content":"Uses tabs.\n",
  "expected_version":"<V_DEFAULT>"}}}'
```

Override the default in one project only. Write the project version under a
temporary name, then move it onto the default's path:

```bash
mcp_call '{"jsonrpc":"2.0","id":304,"method":"tools/call","params":{"name":"file_write","arguments":{
  "project":"web","path":"/users.md.web","content":"Uses two spaces.\n","create_only":true}}}'
# -> {"version":"<V_DRAFT>", ...}
mcp_call '{"jsonrpc":"2.0","id":305,"method":"tools/call","params":{"name":"file_rename","arguments":{
  "project":"web","from_path":"/users.md.web","to_path":"/users.md","overwrite":true,
  "expected_version":"<V_DRAFT>","expected_destination_version":"<V_DEFAULT_LATEST>"}}}'
```

Writing straight to `/users.md` in `web` would edit the shared default instead.
To return `web` to the default later, delete its override with the override's
version. The default appears again.

### 9.4 Search, history and isolation

- `file_search` in a project searches its own files plus the defaults it does not
  override. With `project="*"`, results are per physical project and defaults are
  labelled `"project": "@DEFAULT"`.
- `file_list_versions` for a path in a project lists the history of both the
  project's file and the default at that path. A deleted default stays
  recoverable: `file_restore_version` through a project creates the file in that
  project (no file is visible there any more), while restoring through `@DEFAULT`
  brings the default back for every project.
- The console's project picker lists `@DEFAULT` once it holds files. You can also
  type it manually.
- "Global" always means one API key. Defaults of one key are invisible to every
  other key.
- The memory tools' default project name `default` is unrelated to `@DEFAULT`.
