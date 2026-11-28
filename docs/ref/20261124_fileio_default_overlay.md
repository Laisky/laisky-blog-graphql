# FileIO `@DEFAULT` overlay: source notes (2026-11-24)

Primary-source facts behind [`docs/proposals/fileio_default_project.md`](../proposals/fileio_default_project.md).
Pages were accessed on 2026-11-24 unless a section names 2026-11-27. Where a fact was checked by running a database,
the version is named.

The OverlayFS model below applies only to the FileIO filesystem view (stat, read, list and existing-file
resolution). Retrieval is deliberately not a view: `file_search` reads the project and `@DEFAULT` as two physical
projects, and every hit names the project that stores it.

## Linux OverlayFS (the model we borrow and where we deviate)

Source: <https://docs.kernel.org/filesystems/overlayfs.html> (kernel docs, 7.4.0-rc4).

- **Lookup.** When a name exists in both layers, the upper object is shown and the lower one is hidden. If both are directories, they merge. Only the *names* in a merged directory are merged. Its metadata comes from the upper layer.
  - We apply the same name rule. Our directories are implicit (a name is a directory when files lie below it), so an upper directory hides a lower file of the same name but merges with a lower directory.
- **Copy-up.** Opening a lower file for write, or changing its metadata, first copies it to the upper layer.
  - We deliberately use **write-through** instead, because a change to a default must be global.
- **Whiteouts and opaque directories** let `rm`/`rmdir` hide lower entries without changing the lower layer.
  - We have none: deleting an override reveals the default again.
- **Directory rename.** Renaming a lower or merged directory fails with `EXDEV` unless `redirect_dir` is enabled.
  - A same-project rename instead moves every visible file within its own layer. Directory moves are internal-only in this service.
- **Changing the lower layer while mounted** is "not allowed … the behavior of the overlay is undefined".
  - We avoid this by computing the merged view per request from committed rows.

## Docker overlay2

Source: <https://docs.docker.com/engine/storage/drivers/overlayfs-driver/>

- **Reads** of a file that is not in the container layer come from the image (`lowerdir`).
- **First write** performs a whole-file `copy_up`.
- **Deletes** create a whiteout for a file and an opaque directory for a directory.
- **Directory `rename(2)`** works only when the source and destination are both in the top layer. Otherwise it fails with `EXDEV`.

## PostgreSQL advisory locks

Sources: <https://www.postgresql.org/docs/current/explicit-locking.html> §13.3.4–13.3.5 and <https://www.postgresql.org/docs/current/functions-admin.html> §9.29.10 (current docs = PostgreSQL 19).

- **Release.** Transaction-level advisory locks are released automatically at commit or rollback. There is no explicit unlock.
- **Try-lock.** `pg_try_advisory_xact_lock(bigint)` takes the lock and returns `true`, or returns `false` immediately.
- **Shared variants** conflict only with exclusive holders.
- **Lock order.** The docs advise acquiring locks on multiple objects in a consistent order to avoid deadlocks. PostgreSQL detects deadlocks and aborts one transaction.
- **Verified on PostgreSQL 18.6:**
  - One transaction can hold several advisory keys at once, and commit or rollback releases all of them.
  - Opposite acquisition orders deadlock (`ERROR: deadlock detected`).
- **Superseded decision.** FileIO now takes one tenant-wide advisory lock; project lock ordering no longer applies. See [the current FileIO architecture](../arch/mcp_files.md#102-mutation-admission).

## SQL `LIKE` escaping and case

- **PostgreSQL** (<https://www.postgresql.org/docs/current/functions-matching.html> §9.7.1): `_` matches any single character. The default escape character is backslash, and `ESCAPE '\'` is accepted. `ILIKE` is the case-insensitive variant, so `LIKE` under a deterministic collation is case-sensitive (rechecked 2026-11-27).
- **SQLite** (<https://www.sqlite.org/lang_expr.html#like>): `_` also matches any single character, and there is **no default escape**. `ESCAPE` must be explicit.
- **SQLite case folding** (same page, accessed 2026-11-27): "SQLite only understands upper/lower case for ASCII characters by default", and `'a' LIKE 'A'` is true. The `case_sensitive_like` pragma can make `LIKE` case-sensitive.
- **Verified on SQLite 3.45.1:**
  - `'a_b' LIKE 'a\_b'` → 0 without `ESCAPE`, and 1 with `ESCAPE '\'`.
  - `'/Notes/a' LIKE '/notes/%'` → 1, while `substr('/Notes/a', 1, 7) = '/notes/'` → 0 (2026-11-27).
- **Consequence for us.** Path prefixes escape `\ % _`, always append `ESCAPE '\'`, and add a `substr` equality, so a prefix is literal and case-sensitive on both databases. `_` is a legal path character, so an unescaped `/my_dir/%` also matched `/myXdir/...`, and SQLite matched `/Notes` against `/notes/...`.

## Read snapshots (accessed 2026-11-27)

- **PostgreSQL** (<https://www.postgresql.org/docs/current/transaction-iso.html> §13.2, PostgreSQL 19 docs): under Read Committed, "two successive `SELECT` commands can see different data" within one transaction. Under Repeatable Read, a query "sees a snapshot as of the start of the first non-transaction-control statement in the transaction", so successive `SELECT`s see the same data. "read-only transactions will never have serialization conflicts."
- **SQLite** (<https://www.sqlite.org/isolation.html>): in WAL mode, a reader "continues to see an unchanging 'snapshot' of the database file as it existed at the moment in time when the read transaction started". In rollback mode, a writer must have every reader out of the database file before it writes, so readers never see partial changes.
- **Consequence for us.** A stat or list of an overlaid project that needs several statements runs them in one read-only transaction (`REPEATABLE READ` on PostgreSQL), so its result equals the view at one instant without taking locks. A single-statement read already sees one snapshot.
