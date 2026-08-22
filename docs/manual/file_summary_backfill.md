# File Summary Backfill Runbook

`Service.BackfillRAGSummaries` is the bounded operator API for legacy RAG rows. It
does not route PageIndex-owned files through RAG; those files must be reindexed by
the PageIndex plugin so their tree and source hash remain authoritative.

## Before starting

- Keep `Search.EnforceSummary` disabled until the target tenant has completed its
  backfill and zero-missing-hit verification.
- Choose one authenticated `APIKeyHash` and one project per run.
- Start with a small `BatchSize` (for example, 50) and monitor worker capacity.

## Run and resume

Call `BackfillRAGSummaries` with `AfterPath` empty for the first page. Persist the
returned `NextPath` durably with the operator job. For every subsequent page, pass
the previous `NextPath` as `AfterPath` until `Done` is true.

The operation is restart-safe:

- hashes are derived from the complete stored bytes;
- deterministic degraded summaries are written before the normal UPSERT job is
  enqueued;
- duplicate active UPSERT jobs for the same content generation are not added;
- replaying a page after a crash skips rows already in a ready/degraded state with
  the same content generation.

## Pause and rollback

Pause by stopping the index workers or by stopping page submission. Do not delete
the backfill rows or index jobs. Resume from the last durable `NextPath`; the
operation is idempotent.

There is no destructive data rollback. A deterministic backfill summary remains
generation-bound and can be replaced by the normal worker when credentials and the
configured summarizer are available. To roll back exposure, keep enforcement off
and restore the previous response gate; do not clear `content_hash` or chunk rows.

## Verification

For each completed project, verify that active RAG rows have matching
`content_hash` and `summary_content_hash`, and that `summary_status` is `ready` or
`degraded`. Drain UPSERT jobs, run `file_search` with enforcement in a staging
environment, and confirm no returned hit has an empty or generation-mismatched
`file_summary` before enabling enforcement for production.

Record the tenant, project, cursor pages, processed/enqueued counts, worker drain
time, degraded count, and operator identity with the deployment change.
