# FileIO `@DEFAULT` project: a per-API-key default layer

Status: implemented. This document is the binding contract for the `@DEFAULT`
layer. Code: `internal/mcp/files/overlay*.go`, `service_rename.go` and
`service_search.go`. User guide:
[`docs/manual/mcp_files.md` §9](../manual/mcp_files.md#9-shared-defaults-the-default-project).
Research notes: [`docs/ref/20261124_fileio_default_overlay.md`](../ref/20261124_fileio_default_overlay.md).

## 1. Goal

An API key owns many FileIO projects. Some files belong in all of them, for
example an agent's user preferences (`/users.md`). Before this change, such a
file had to be copied into every project and the copies drifted apart.

`@DEFAULT` is a reserved project that acts as a base layer under every other
project of the same API key, similar to the lower layer of an OverlayFS mount:

- A project's filesystem view shows its own entry at a name, and the `@DEFAULT`
  entry otherwise (§3).
- Inherited files are read, listed, versioned, written, renamed and deleted
  through the project like its own files.
- Changing an inherited file changes it in `@DEFAULT`, so the change is global for
  that API key.
- Retrieval is not a view. `file_search` finds documents of both physical projects
  and names the project that stores each hit (§7).
- Tenants stay strictly isolated. "Global" means one API key (`apikey_hash`).
  Another key never reads, lists, searches or changes these files.

## 2. Names and scope

- The name is exactly `@DEFAULT`, case-sensitive. `ValidateProject` accepts it in
  addition to the ordinary `[A-Za-z0-9_.-]{1,128}` rule. Every other name containing
  `@` stays invalid, so no existing project can collide and `@` stays reserved for
  future built-in projects.
- Addressing `@DEFAULT` is **physical only**: operations see and change only its own
  files. It is how defaults are seeded and managed.
- Every other user project `P` is **overlaid**: its filesystem view is `P` (upper
  layer) over `@DEFAULT` (lower layer).
- Never overlaid: the `file_search` wildcard `project="*"`, system namespaces
  (`system_owner <> ''`, such as PageIndex catalogs), and internal workers (index
  worker, summary refresh, backfill), which address physical rows.
- Storage is unchanged: `@DEFAULT` rows are ordinary `mcp_files` rows with
  `project = '@DEFAULT'` under the same `apikey_hash`. No migration is needed.

## 3. Filesystem view

The view of an overlaid project `P` serves `file_stat`, `file_read` and
`file_list`, and resolves every existing-file operand: the file a write, append,
delete, restore or same-project rename addresses, and the file a version
condition is checked against.

A directory is the recursive union of both layers' children. Only a
**conflicting name** is replaced by `P`'s entry:

- a `P` file and a default file at the same path,
- a `P` file and a default directory at the same path,
- a `P` directory and a default file at the same path.

So a `P` file at `X` hides the default file at `X` and the whole default subtree
below `X`. A `P` directory `X` (`P` has files below `X`) hides a default **file**
at `X`, but not the default files below `X`: their children merge.

| `@DEFAULT` holds | `P` holds | `P` shows |
| --- | --- | --- |
| `/notes/a.md` | `/notes/b.md` | `/notes/a.md` (default), `/notes/b.md` |
| `/notes/a.md`, `/notes/b.md` | `/notes/a.md/x` | `/notes/a.md/x`, `/notes/b.md` (default) |
| `/notes/a.md`, `/notes/b.md` | file `/notes` | `/notes` only |

- Directories stay implicit: a directory exists exactly when a visible file lies
  below it. There are no directory records, so an empty directory cannot exist.
- Path comparisons are case-sensitive and segment-aware on SQLite and
  PostgreSQL: `/Notes` is not `/notes`, and `/notes` does not contain `/notes2/x`.
- A version token (`incarnation:revision`) identifies a physical row, so a token
  read from a default never matches a project file at the same path.
- Consistency: a read resolves its file in one SQL statement. A stat or list that
  needs more than one statement runs in one read-only snapshot transaction
  (`REPEATABLE READ` on PostgreSQL). Reads take no locks, and every result equals
  the view at one instant.

## 4. Mutations (write-through)

| Operation in `P` | Effect |
| --- | --- |
| Any write mode, or restore, of a path that resolves to a default | Changes that `@DEFAULT` row in place, for every project of the key. |
| Write that creates a new name | Creates the file in `P`. A new file reaches `@DEFAULT` only when `@DEFAULT` is addressed. |
| Delete of a path that resolves to a default | Deletes the default for every project. Its history is kept, so it is recoverable (§6). |
| Delete of `P`'s file that overrides a default | Deletes `P`'s row. The default is visible again ("reset to default"). |
| Recursive directory delete (internal callers only) | Deletes every visible file below the path, each in its own physical project. Defaults that `P` hid stay and become visible. |

- A new name must be legal in the view: no visible file below it
  (`IS_DIRECTORY`) and no visible file at an ancestor (`NOT_DIRECTORY`).
- Version conditions are checked against the file `P` sees, inside the locked
  transaction. A token read from a default conflicts once an override appears,
  and vice versa.

### Why write-through rather than copy-up

OverlayFS and Docker `overlay2` copy a lower file up into the upper layer on first
write, and record deletions as whiteouts. This design deliberately does neither:

- The requirement is that a change to a default is global. Copy-up would fork a
  private copy per project, which is the drift `@DEFAULT` exists to remove.
- Without whiteouts, deleting an override simply reveals the default again. That is
  the "project has it → show it; otherwise show the default" rule applied literally.
  It also needs no new storage concept.
- OverlayFS calls changing the lower layer while mounted undefined behaviour.
  Here the view is computed per request from committed rows, so changes to
  `@DEFAULT` are visible immediately and consistently in every project.

## 5. Rename

### 5.1 Same-project rename

This applies when `target_project` is omitted, empty, or equal to `project`.

- **Source:** the file `P` sees at `from_path` (for an internal directory move,
  every file `P` sees below it). Each source is renamed within its own physical
  project, so renaming a default renames it for every project.
- **Destination:** judged on `P`'s view. A visible file at an ancestor fails with
  `NOT_DIRECTORY`. Visible files below a file destination, or a visible file at the
  destination without `overwrite=true`, fail with `ALREADY_EXISTS`. The source's
  physical project must also stay self-consistent; for example, a default cannot
  move below a default file that `P` hides (`NOT_DIRECTORY`).
- **Overwrite replaces only what `P` sees:**
  - A visible destination in the source's own project is replaced. It is
    snapshotted into history first.
  - A `P` file moved onto a visible default **shadows** it. The default stays
    unchanged for every other project, and `P` now shows its own file. This is the public override
    recipe (§10).
  - A default moved onto a `P` file deletes that `P` file, because it would hide
    the moved default.
- A rename never replaces a file `P` cannot see. When a default that `P` hides also
  occupies the destination, the rename fails with `ALREADY_EXISTS`: its version was
  never visible to condition on.

### 5.2 Cross-project rename

This applies when `target_project` names another project. It is the #106 move.

- **Source:** the physical file a normal read of `project` resolves, that is,
  `P`'s own file or the inherited `@DEFAULT` file (for an internal directory move,
  every file `P` sees below `from_path`).
- **Destination:** the explicit physical `target_project`/`to_path`. Collisions,
  `overwrite`, `expected_destination_version`, `destination_must_not_exist` and
  the quota are judged on `target_project`'s own physical files only.
- A `@DEFAULT` file at the destination path is not a collision of an ordinary
  target project. The moved file becomes that project's own file and shadows the
  default there. The default is never overwritten or deleted as a side effect.
- **Moving an inherited file is a move, not a copy.** The file leaves `@DEFAULT`
  and disappears from every project that inherited it.
- `target_project="@DEFAULT"` moves a project file into the shared layer. Its
  destination is judged on `@DEFAULT`'s physical files.
- The row keeps its id and incarnation. Its history, index jobs, PageIndex trees and
  catalog entries move with it in one transaction, with the same locking, version
  checks, quotas, history and plugin hooks as every #106 move.
- **Locks:** `project` and `target_project`, plus `@DEFAULT` when the default layer
  touches the source path, always acquired in `ProjectLockOrder` (§9). The source
  is re-resolved and validated under those locks.

## 6. History

`file_list_versions`, `file_read_version` and the HTTP history endpoints of an
overlaid project return the union of the path's history in `P` and in `@DEFAULT`,
ordered by snapshot ID. IDs are unique across projects. A deleted default
therefore stays recoverable through any project. `file_restore_version` (and
`RestoreVersion`) reads the snapshot from either layer and writes it like any
write: in place into the visible file's physical project, or into `P` when no
file is visible at the path. To bring back a deleted default for every project,
restore it through `@DEFAULT`.

## 7. Retrieval is physical (RAG and PageIndex)

Search does not apply the filesystem view.

- `file_search` of an ordinary project `P` searches the two physical projects `P`
  and `@DEFAULT` of the same tenant. Every document of either project is a
  candidate: defaults that `P`'s view hides, defaults below a `P` file, and two
  documents at the same path are all separate eligible hits. Normal ranking and
  the candidate and LLM budgets apply to the combined candidates.
- Every hit of such a search names its physical project in the existing
  `project` field (`"P"` or `"@DEFAULT"`). `file_read` with `project` set to that
  value returns exactly that document; `project="@DEFAULT"` returns the default
  even where `P` overrides the path.
- Search results do not change when an override is created or deleted.
- A search of `@DEFAULT` itself or of a system namespace is single-project and
  physical; its hits carry no `project` field. The wildcard `*` is unchanged: all
  projects of the caller, every hit labelled.
- `path_prefix` is a literal, case-sensitive string prefix.
- Indexing and summaries are physical. A default is chunked, embedded, summarized
  and PageIndex-indexed once, under `@DEFAULT`, never per inheriting project.
  `WriteResult.ResolvedProject` (internal, never on the wire) names the physical
  project a write stored, and plugins index that project.

## 8. Automated project-local state

The memory engine (`memory_before_turn`, `memory_after_turn`, and maintenance:
retention, compaction and rewrites under `/memory/...`) and the shadow plugin's
replay address physical projects only. They never read, rewrite, archive or
delete a shared default as if it were project state. The memory tools' implicit
project `default` (lowercase) is an ordinary project unrelated to `@DEFAULT`.

## 9. Concurrency

- A mutation of an overlaid project first takes the locks it needs without the
  default layer (`P`, plus `target_project` for a move). Inside that transaction it
  probes whether `@DEFAULT` holds a live file at, above or below the touched paths.
  If not, the mutation runs the unchanged single-project path and never reads or
  changes `@DEFAULT` files. If a default appears concurrently, the result equals
  running this mutation first. (A restore may still read an immutable `@DEFAULT`
  history snapshot; snapshots are never edited in place.)
- Otherwise the attempt rolls back before any change and reruns holding
  `@DEFAULT` too. Every multi-project mutation acquires its scopes in
  `ProjectLockOrder` (ascending advisory key, then name), so two mutations over the
  same scopes cannot each hold one and wait for the other.
- A mutation of several scopes requires a `MultiProjectLockProvider`. A configured
  `LockProvider` without it refuses the mutation rather than leave one scope
  unserialized.
- Reads take no locks (§3).

## 10. Public usage recipes

- **Seed or edit a default for every project:** write it with `project="@DEFAULT"`,
  or edit it through any project.
- **Override a default for one project:** write the project's version under a
  temporary name (`create_only=true`), then `file_rename` it onto the default's path
  with `overwrite=true`, `expected_version` of the temporary file and
  `expected_destination_version` of the default. Writing directly to the path would
  edit the default. A project file that existed before the default was created
  also overrides it.
- **Reset an override:** delete the project's file. The default reappears.
- **Find where a file is stored:** `file_search` hits name the physical project;
  `file_list`/`file_read` with `project="@DEFAULT"` show the defaults themselves.
- **Give one project its own copy of an inherited file:** read it and apply the
  override recipe with that content. A cross-project `file_rename` of an inherited
  file removes it from `@DEFAULT` instead.

## 11. Compatibility

- A mutation whose paths no default touches keeps the previous single-project
  code path, after one bounded (`LIMIT 1`) probe. Reads of an overlaid project
  consider both layers in the same statements; a multi-statement stat or list
  runs in a read-only transaction.
- **Search result shape:** a `file_search` of an ordinary project now sets
  `project` on every hit; before, only `project="*"` did. No new field was added.
- Apart from #106's `target_project`, no tool argument or GraphQL type changed.
  Tool, GraphQL and server-card descriptions mention `@DEFAULT`, the physical
  search scope and the physical move destination. Project discovery lists `@DEFAULT` once it has live
  files, and accepts `@` in search fragments.
- Pre-existing defects fixed in the same change, for every project:
  - An overwriting rename discarded the replaced destination's content without a
    history snapshot. It is now snapshotted.
  - Directory and search prefixes used unescaped `LIKE`, so `_` (a legal path
    character) matched any character and `/my_dir` matched `/myXdir/...`. Prefixes
    are now escaped with `ESCAPE '\'`.
  - SQLite `LIKE` folds ASCII case, so `/Notes` matched `/notes/...` in stat, list,
    write conflict checks, delete, rename and search prefixes. Every prefix
    predicate now adds a case-sensitive comparison.

## 12. What the review removed

The review replaced view-filtered retrieval with physical retrieval, which
removed the correlated shadow predicate in RAG retrieval and its existence
probe, and the PageIndex fallback and shadowed-catalog merge. The
overlay-specific rename planner (its merged and physical conflict planners)
gave way to the ordinary move, run once per physical project. `SystemStateHooks`
and `PathMove` gave way to #106's change-based plugin state plan, and the
duplicated physical delete-target helpers gave way to one view-aware resolver.
