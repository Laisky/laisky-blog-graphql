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
      - [Example B: move a file into another project](#example-b-move-a-file-into-another-project)
  - [6. Common error codes](#6-common-error-codes)
  - [7. Minimal end-to-end flow](#7-minimal-end-to-end-flow)
  - [8. Integration best practices](#8-integration-best-practices)
  - [9. Shared defaults: the `@DEFAULT` project](#9-shared-defaults-the-default-project)
    - [9.1 How a project sees `@DEFAULT`](#91-how-a-project-sees-default)
    - [9.2 What changes are global](#92-what-changes-are-global)
    - [9.3 Recipes](#93-recipes)
    - [9.4 Search](#94-search)
    - [9.5 History and isolation](#95-history-and-isolation)

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
  - `path_prefix`: literal, case-sensitive prefix filter, for example `/docs`;
    a missing leading `/` is added automatically
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
      "project": "demo",
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
- `project` names the project that stores the chunk's file. A search of an ordinary project also covers your `@DEFAULT` files, so its hits name either that project or `"@DEFAULT"` ([section 9.4](#94-search)). With `project="*"`, hits name any of your projects. Only a search of `@DEFAULT` itself omits the field.

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
  - `overwrite`: default `false`. It lets a single file replace an existing file at `to_path`. It never applies
    to a directory: a directory move with `overwrite=true` fails with `INVALID_ARGUMENT`.
  - `target_project`: destination project for a cross-project move. Omit it or pass `""` to rename within `project`.
    `to_path`, the collision and overwrite rules, `expected_destination_version` and `destination_must_not_exist`
    all apply in `target_project`, and the moved bytes count against its storage quota. The file keeps its identity
    and version history; its revision advances, so re-read before editing it again. `"*"` is rejected.
    A `@DEFAULT` file at `to_path` is not a collision: the moved file overrides it in `target_project`. A file that
    `project` inherits from `@DEFAULT` is moved out of `@DEFAULT`, not copied ([section 9.2](#92-what-changes-are-global)).

The public `file_rename` tool and GraphQL `FileRename` move one file: they require its `expected_version`, and a
file token never authorizes a directory-wide mutation (see [FileIO versioning](fileio_versioning.md)). Directory
moves made by the FileIO service follow these rules.

A directory move is a relocation, never a merge or a replacement. `to_path` must be vacant in the destination
project: no file at `to_path`, no file anywhere below it (an existing directory is occupied even when its children
have other names) and no file at an ancestor. An occupied destination fails with `ALREADY_EXISTS` (`NOT_DIRECTORY`
for a file ancestor) and changes nothing. Within one project, a directory cannot move into its own subtree
(`INVALID_PATH`) or into its own parent or another ancestor, which already holds it (`ALREADY_EXISTS`). Every moved
file's new path must also be a valid path, including the 512-byte limit (`INVALID_PATH`). Moving a path onto itself
in the same project is a no-op; the same path in another project is a real move.

A rename never waits for indexing and never fails because of a search index. Any extension change the path
rules allow is accepted (for example `.txt -> .md`, `.md -> .pdf` or `.md -> .txt`), and the bytes are never
converted. RAG updates its index for the moved paths in the background, so allow a short delay before searching
them. The PageIndex backend is disabled; see [MCP memory plugins](mcp_memory_plugins.md#6-pageindex-phase-2).

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

#### Example B: move a file into another project

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

If `demo` inherits `/docs/guide.txt` from `@DEFAULT`, this call moves the file out of `@DEFAULT`, so no other
project inherits it afterwards.

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

# 4) Rename
mcp_call '{"jsonrpc":"2.0","id":204,"method":"tools/call","params":{"name":"file_rename","arguments":{"project":"demo","from_path":"/quick/start.txt","to_path":"/quick/start-renamed.txt","overwrite":false}}}'

# 5) Search
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
project. Exact rules: [`docs/proposals/fileio_default_project.md`](../proposals/fileio_default_project.md).

### 9.1 How a project sees `@DEFAULT`

- If the project has its own file at a path, you see the project's file.
  Otherwise, if `@DEFAULT` has a file there, you see the default.
- Directories merge: a directory shows the files of both. Only a name that
  conflicts is replaced by the project's entry:
  - A project file `/notes` hides the default file `/notes` and every default
    file below `/notes/`.
  - A project directory `/notes` (any project file below it) hides a default
    *file* `/notes`. Default files below `/notes/` still show.
- Example: with the default `/notes/a.md` and the project file `/notes/b.md`,
  `file_list` of `/notes` shows both files.
- Names are case-sensitive: `/Notes` and `/notes` are different paths.

`file_read`, `file_stat`, `file_list` and the history tools treat inherited files
exactly like the project's own files. They return no extra field, and `file_list`
shows each name once. The history tools show the history of the file you see at
the path (section 9.5). `@DEFAULT` itself is an ordinary project: addressing it
shows only its own files.

### 9.2 What changes are global

| You do this in a project | Result |
| --- | --- |
| Edit (any write mode) or restore a file that comes from `@DEFAULT` | The default changes for **every** project. |
| Create a new file | The file is created in that project only. |
| Delete a file that comes from `@DEFAULT` | The default is deleted for **every** project. Its history is kept in `@DEFAULT`. |
| Delete the project's own override of a default | The default becomes visible again. |
| Rename a file that comes from `@DEFAULT` within the project | It is renamed for every project. |
| Rename a project file onto a default's path with `overwrite=true` | The project file overrides the default in that project only. |
| Move a file that comes from `@DEFAULT` to another project (`target_project`) | The file **leaves** `@DEFAULT`. It is moved, not copied, and no project inherits it afterwards. |
| Move a file to a path where `target_project` only inherits a default | The moved file overrides that default in `target_project`. The default stays. |
| Move a project file with `target_project="@DEFAULT"` | It becomes a default for every project. |

All the usual version rules apply. A version from `file_read`/`file_stat` names
the exact file you saw, so an edit fails with `VERSION_CONFLICT` if another
project changed the default in the meantime. An overwriting rename replaces only
files you can see: if a default your project hides also occupies the
destination, the rename fails with `ALREADY_EXISTS`.

A directory move (section 5.7) relocates the directory only to a vacant path, never
merging it into an existing one. Within a project, the path must be vacant in what
you see, so a default shown below the destination occupies it. A move to another
project checks only that project's own files, so a default it merely inherits there
does not block the move. An `APPEND` or any other write runs exactly once, on the
file you see.

### 9.3 Recipes

Seed a default (once, from anywhere):

```bash
mcp_call '{"jsonrpc":"2.0","id":301,"method":"tools/call","params":{"name":"file_write","arguments":{
  "project":"@DEFAULT","path":"/users.md","content":"Prefers concise English answers.\n","create_only":true}}}'
```

Every project now inherits it:

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

Reset the override. Delete the project's file with its current version (from
`file_stat` in `web`); the default appears again:

```bash
mcp_call '{"jsonrpc":"2.0","id":306,"method":"tools/call","params":{"name":"file_delete","arguments":{
  "project":"web","path":"/users.md","expected_version":"<V_OVERRIDE>"}}}'
```

Moving an inherited file to another project takes it out of `@DEFAULT` for
everyone. After this call, `archive` owns `/users.md`, `@DEFAULT` no longer has
it, and only projects with their own `/users.md` still show one:

```bash
mcp_call '{"jsonrpc":"2.0","id":307,"method":"tools/call","params":{"name":"file_rename","arguments":{
  "project":"graphql","from_path":"/users.md","target_project":"archive","to_path":"/users.md",
  "expected_version":"<V_DEFAULT_LATEST>"}}}'
```

To give one project its own copy while every other project keeps the default,
use the override recipe instead. To share a project file with every project,
move it with `"target_project":"@DEFAULT"`.

### 9.4 Search

`file_search` in a project searches two projects: the project's own files and
**all** `@DEFAULT` files, including defaults the project overrides. A project file
and a default at the same path are two separate hits. Every hit names the
project that stores it. For example, a search in `web` while it overrides
`/users.md` (other chunk fields as in [section 5.5](#55-file_search)):

```json
{
  "chunks": [
    { "project": "web", "file_path": "/users.md", "chunk_content": "Uses two spaces.\n", "score": 0.91 },
    { "project": "@DEFAULT", "file_path": "/users.md", "chunk_content": "Prefers concise English answers.\nUses tabs.\n", "score": 0.88 }
  ]
}
```

- To open a hit, call `file_read` with the hit's `project` and `file_path`.
  `project="@DEFAULT"` returns the default even where your project overrides it.
- Creating or deleting an override does not change search results.
- A search of `@DEFAULT` itself returns only defaults and omits `project`.
  `project="*"` is unchanged: it searches every project and labels every hit.

### 9.5 History and isolation

- History belongs to the file you see at a path, never to both layers at once:
  - If the project has its own file at the path (an override), the history tools
    show only the project file's history.
  - If the path shows a default, they show only the default's history.
  - Deleting an override shows the default again, with the default's history.
    The override's history is kept but no longer shown at that path.
  - `project="@DEFAULT"` always shows the default's own history.
- A `history_id` of the other layer is refused with `NOT_FOUND` by
  `file_read_version` and `file_restore_version`, and nothing changes. Use IDs
  from `file_list_versions` of the same `project` and `path`.
- `file_restore_version` writes into the file the history belongs to: the
  project's file, or the default for every project.
- Deleted files: when no file is visible at the path, the history tools show the
  history of the project you name. Restore a deleted project file through that
  project with `create_only=true`. Restore a deleted default through
  `project="@DEFAULT"`, which brings it back for every project. A project that
  never had its own file at the path shows no history there.
- The console's project picker lists `@DEFAULT` once it holds files. You can also
  type it manually.
- "Global" always means one API key. Defaults of one key are invisible to every
  other key.
- The memory tools' default project name `default` is unrelated to `@DEFAULT`.
  Memory maintenance changes only the project's own files, never a shared default.
