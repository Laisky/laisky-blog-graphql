# SQL ownership gate portability validation (2026-10-08)

The ownership gate must enforce the same tracked-table predicate or explicit
annotation policy under GNU awk and mawk. The old dynamic regex used a GNU
word-end operator; mawk 1.3.4 matched zero candidate lines and incorrectly passed.

The [mawk manual](https://invisible-island.net/mawk/manpage/mawk.html) documents
its supported regular-expression operators. The portable suffix
`([^[:alnum:]_]|$)` preserves the tracked table boundary on both tested tools.

Retained fixtures require scoped and annotated queries to pass, unowned queries
and empty scans to fail, and fixture source to remain unchanged. Before the fix,
the same fixtures failed; after the fix, GNU awk 5.2.1 and mawk 1.3.4 passed.
The committed baseline contains 87 candidate SQL statements; all pass the
ownership gate under either implementation after this fix.
