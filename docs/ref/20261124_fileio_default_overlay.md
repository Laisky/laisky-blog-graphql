# FileIO `@DEFAULT` overlay: source notes (2026-11-24)

Primary-source facts behind [`docs/proposals/fileio_default_project.md`](../proposals/fileio_default_project.md).
All pages were accessed on 2026-11-24. Where a fact was checked by running a database, the version is named.

## Linux OverlayFS (the model we borrow and where we deviate)

Source: <https://docs.kernel.org/filesystems/overlayfs.html> (kernel docs, 7.4.0-rc4).

- **Lookup.** When a name exists in both layers, the upper object is shown and the lower one is hidden. If both are directories, they merge. Only the *names* in a merged directory are merged. Its metadata comes from the upper layer.
- **Copy-up.** Opening a lower file for write, or changing its metadata, first copies it to the upper layer.
  - We deliberately use **write-through** instead, because a change to a default must be global.
- **Whiteouts and opaque directories** let `rm`/`rmdir` hide lower entries without changing the lower layer.
  - We have none: deleting an override reveals the default again.
- **Directory rename.** Renaming a lower or merged directory fails with `EXDEV` unless `redirect_dir` is enabled.
  - We move every visible file within its own layer instead. Directory moves are internal-only in this service.
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
- **Consequence for us.** Every multi-project FileIO mutation acquires its scopes in `ProjectLockOrder`: ascending advisory key, then name.

## SQL `LIKE` escaping

- **PostgreSQL** (<https://www.postgresql.org/docs/current/functions-matching.html> §9.7.1): `_` matches any single character. The default escape character is backslash, and `ESCAPE '\'` is accepted.
- **SQLite** (<https://www.sqlite.org/lang_expr.html#like>): `_` also matches any single character, and there is **no default escape**. `ESCAPE` must be explicit.
- **Verified on SQLite 3.45.1:** `'a_b' LIKE 'a\_b'` → 0 without `ESCAPE`, and 1 with `ESCAPE '\'`.
- **Consequence for us.** Path prefixes escape `\ % _` and always append `ESCAPE '\'`. `_` is a legal path character, so an unescaped `/my_dir/%` also matched `/myXdir/...`.
