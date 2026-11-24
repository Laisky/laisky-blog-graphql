# FileIO `@DEFAULT` project: a per-API-key default layer

Status: implemented. This document is the binding contract for the `@DEFAULT`
overlay. Code: `internal/mcp/files/overlay*.go`. User guide:
[`docs/manual/mcp_files.md` §9](../manual/mcp_files.md#9-shared-defaults-the-default-project).
Research notes: [`docs/ref/20261124_fileio_default_overlay.md`](../ref/20261124_fileio_default_overlay.md).

## 1. Goal

An API key owns many FileIO projects. Some files belong in all of them, for
example an agent's user preferences (`/users.md`). Before this change, such a
file had to be copied into every project and the copies drifted apart.

`@DEFAULT` is a reserved project that acts as a base layer under every other
project of the same API key, similar to the lower layer of an OverlayFS mount:

- A project shows its own file when it has one, and the `@DEFAULT` file otherwise.
- A user of a project does not need to know `@DEFAULT` exists. Inherited files are
  read, listed, searched, versioned, written, renamed and deleted exactly like the
  project's own files.
- Changing an inherited file changes it in `@DEFAULT`, so the change is global for
  that API key.
- Tenants stay strictly isolated. "Global" means one API key (`apikey_hash`).
  Another key never reads, lists, searches or changes these files.

## 2. Names and scope

- The name is exactly `@DEFAULT`, case-sensitive. `ValidateProject` accepts it in
  addition to the ordinary `[A-Za-z0-9_.-]{1,128}` rule. Every other name containing
  `@` stays invalid, so no existing project can collide and `@` stays reserved for
  future built-in projects.
- `@DEFAULT` itself is an ordinary **physical** project. Operations addressed to it
  see and change only its own files. It is how defaults are seeded and managed.
- Every other user project is **overlaid**: it reads the merged view of itself
  (upper layer) over `@DEFAULT` (lower layer).
- Never overlaid: the `file_search` wildcard `project="*"` (each physical project,
  including `@DEFAULT`, appears once and is labelled), system namespaces such as the
  PageIndex catalog (`system_owner <> ''`), and internal workers (index worker,
  summary refresh, backfill), which always address physical rows.
- The memory tools' implicit project `default` (lowercase) is unrelated; it is an
  ordinary project that also inherits `@DEFAULT`.
- Storage is unchanged: `@DEFAULT` rows are ordinary `mcp_files` rows with
  `project = '@DEFAULT'` under the same `apikey_hash`. No migration is needed.

## 3. Visibility rule

For an overlaid project `P`, the visible files are:

1. every live file of `P`, and
2. every live `@DEFAULT` file `x` such that `P` has no live file at `x`, at an
   ancestor of `x`, or below `x`.

Rule 2 makes whole names win, not just identical paths. A file in `P` hides a
default directory with the same name, and a directory in `P` (any file below the
name) hides a default file with that name. Directories remain implicit: a
directory exists exactly when some visible file lies below it.

The rule is applied identically by `file_stat`, `file_read`, `file_list`,
`file_search` (indexed retrieval and the raw-file fallback), PageIndex search and
mutation conflict checks. A file's version token (`incarnation:revision`)
identifies its physical row, so a token read from a default is a different token
from any project override at the same path.

## 4. Mutation routing (write-through)

| Operation in `P` | Target |
| --- | --- |
| Write/append/overwrite/truncate/restore of a path that resolves to a default | The `@DEFAULT` row, in place. The change is global. |
| Write that creates a new name | `P`. New files never land in `@DEFAULT` unless `@DEFAULT` is addressed. |
| Delete of a path that resolves to a default | The `@DEFAULT` row. It disappears from every project that does not override it. |
| Delete of a `P` file that overrides a default | The `P` row. The default becomes visible again ("reset to default"). |
| Recursive directory delete (internal callers only) | Every visible file below the path, each in its own layer. Hidden defaults remain and become visible. |
| Rename of a file | The move happens inside the source file's own layer. Renaming a default renames it for every project. |
| Rename of a directory (internal callers only) | Each visible file moves inside its own layer. Hidden defaults keep their names. |

Conflict checks use the merged view of `P`:

- A new file name must not have visible descendants (`IS_DIRECTORY`) or a visible
  file at an ancestor (`NOT_DIRECTORY`).
- A rename destination must not be visible (`ALREADY_EXISTS`), unless it is a file
  and `overwrite=true`.
- Each physical layer must also stay self-consistent. For example, a default cannot
  be renamed under a name that is a hidden default *file* of `@DEFAULT`
  (`NOT_DIRECTORY`, "parent path is a file in the default layer").

An overwriting file rename removes the files that would block or hide the moved
file:

- A `P` file at the destination is deleted. It would otherwise hide a moved default,
  or it collides physically with a moved `P` file.
- A `@DEFAULT` file at the destination is deleted only when the source is also a
  default (physical collision in `@DEFAULT`), including a default hidden by a `P`
  override.
- A `P` file moved over a default **shadows** it instead of deleting it. That
  default stays global for the other projects. This is the public recipe for
  creating a project-specific override (§7).

Replaced destinations are snapshotted into history before deletion. Their index
delete jobs are enqueued before the moved file's upsert.

### Why write-through rather than copy-up

OverlayFS and Docker `overlay2` copy a lower file up into the upper layer on first
write, and record deletions as whiteouts. This design deliberately does neither:

- The requirement is that a change to a default is global. Copy-up would fork a
  private copy per project, which is the drift `@DEFAULT` exists to remove.
- Without whiteouts, deleting an override simply reveals the default again. That is
  the "project has it → show it; otherwise show the default" rule applied literally.
  It also needs no new storage concept.
- OverlayFS calls changing the lower layer while mounted undefined behaviour.
  Here the merged view is computed per request from committed rows, so changes to
  `@DEFAULT` are visible immediately and consistently in every project.

## 5. History

`file_list_versions`, `file_read_version` and the HTTP history endpoints of an
overlaid project return the union of the path's history in `P` and in `@DEFAULT`,
ordered by snapshot ID. IDs are unique across projects. A deleted default
therefore stays recoverable through any project. `file_restore_version` (and
`RestoreVersion`) reads the snapshot from either layer and writes it like any
write: in place into the visible file's layer, or into `P` when the path is not
visible. To bring back a deleted default for every project, restore it through
`@DEFAULT`.

## 6. Concurrency

- Mutations in `@DEFAULT` lock only `@DEFAULT`. Mutations in an overlaid project
  first take only `P`'s lock and, **inside that transaction**, check whether any
  live default lies at, above, or below the mutated paths
  (`defaultLayerTouches`). If none does, the mutation runs the unchanged
  single-project code and never reads or changes `@DEFAULT` files. (A restore may
  still read an immutable `@DEFAULT` history snapshot; snapshots are never edited
  in place.) If a default appears there concurrently, the result equals running
  this mutation first, because it read no default file.
- Otherwise the attempt is rolled back before any change, and the mutation reruns
  in **overlay mode**. Overlay mode holds both `@DEFAULT` and `P` in one transaction,
  acquired in `ProjectLockOrder` (ascending advisory key, then name). Every
  multi-project mutation, including a cross-project move, must use the same global
  order, so two mutations over the same scopes cannot each hold one and wait for
  the other.
- Version conditions are checked against the file the merged view shows, inside
  the locked transaction. A token read from a default conflicts once an override
  appears, and vice versa.
- `LockProvider` implementations used with overlays should implement
  `MultiProjectLockProvider`. Others keep their own first scope, and the
  remaining scopes are taken as PostgreSQL advisory locks on the same transaction.
- Reads take no locks. Each read query sees committed rows, and a multi-query read
  (for example, stat of a missing file followed by a directory probe) can interleave
  with a concurrent commit exactly as before this change.

## 7. Public usage recipes

- **Seed or edit a default for every project:** write it with `project="@DEFAULT"`,
  or edit it through any project.
- **Override a default for one project:** write the project's version under a
  temporary name (`create_only=true`), then `file_rename` it onto the default's path
  with `overwrite=true`, `expected_version` of the temporary file and
  `expected_destination_version` of the default. Writing directly to the path would
  edit the default. A project file that existed before the default was created
  also overrides it.
- **Reset an override:** delete the project's file. The default reappears.
- **Find a default's owner layer:** `file_list`/`file_read` through
  `project="@DEFAULT"`, or `file_search` with `project="*"`, whose hits carry
  `"project": "@DEFAULT"`.

## 8. Plugins and indexing

- RAG: defaults are chunked, embedded and summarized under `project='@DEFAULT'`
  by the existing outbox worker. Overlaid searches admit default chunks by the §3
  rule in SQL (`NOT EXISTS` over the project's live files), so candidate limits
  apply after hiding. Single-project results never carry a `project` field.
- PageIndex: `WriteResult.ResolvedProject` (internal, never on the wire) names the
  physical project that stored a write. Trees, catalogs and summaries are built
  for that project. Transactional deletes and renames receive `SystemStateHooks`
  and update the catalog of each physical project they changed. Search merges the
  `@DEFAULT` catalog under the project's own catalog. Each candidate is then
  validated by reading it through the overlay, so a hidden default never answers.
  A project entry whose source no longer validates (left behind when the file was
  deleted outside PageIndex) yields to the default entry it shadows.

## 9. Compatibility

- API keys without `@DEFAULT` files take exactly the previous code paths. The only
  overhead is a bounded (`LIMIT 1`) existence probe per mutation, per search, per
  directory stat, and an extra indexed lookup on file-read misses.
- With defaults present, overlay work is proportional to the layers involved. A
  listing reads the metadata of both layers below the path. The search and
  directory-stat predicate checks each candidate default row against the
  project's live files with a `NOT EXISTS`. Its ancestor and descendant tests
  are not index-assisted, so cost grows with (default rows considered) ×
  (project files). That is acceptable for a layer meant for a modest set of
  shared files. If profiling ever shows otherwise, the documented alternative is
  a precomputed per-project shadow set.
- No wire field, tool argument or GraphQL type changed. Tool descriptions of the
  `project` argument mention `@DEFAULT`. Project discovery lists `@DEFAULT` once it
  has live files, and accepts `@` in search fragments.
- Fixed in the same change:
  - Directory and search prefixes treat `_` (a legal path character) literally
    (`LIKE ... ESCAPE '\'`). Previously `/my_dir` also matched `/myXdir/...`, which
    could make `stat` report a phantom directory or block a write.
  - An overwriting rename now snapshots the replaced destination into history.
    Previously, in every project, the replaced file's last content was lost.
