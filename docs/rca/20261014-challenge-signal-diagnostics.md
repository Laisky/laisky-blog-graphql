# Retain finite crawler challenge signals

The 2026-10-14 15:48:21 UTC web_fetch incident was correlated by exact task ID with an old-worker render/challenge row about 729.799 ms earlier. Its Redis result was absent beyond the three-hour retention. Target-bearing adjacent rows had no task ID and a different concurrent task was canceled, so they cannot identify this task's target. A genuine upstream challenge versus an old-detector false positive remains unknown. No incident response body, browser trace or detector match was recovered.

The accepted semantic detector narrows the old substring matching but still returned only a boolean. This follow-up preserves every existing detection condition and traversal priority while retaining the first matched finite signal:

- `widget_element`
- `error_element`
- `challenge_platform_script`
- `challenge_options_assignment`

The worker emits `challenge_signal` in its existing operation log and default SDK HTTP alert. Its optional `failure_signal` result extension is emitted only for failed render/challenge results. The producer accepts it only on the exact submitted task, target, output mode and egress policy, validates the finite value again, and emits `worker_signal` through the existing scoped default SDK delivery. A signal reports a detector condition; it does not establish a real challenge, connection evidence or user identity. No response body, HTML marker, resource URL, target, credentials, query or user data is included.

Missing signals remain valid legacy results. The pinned old DTO ignores the additive field, and FailedReason remains `challenge`. Success, nonchallenge, unknown/canary, invalid-stage and mismatched-correlation controls omit signals. Existing challenge rejection, receipt verification, billing, queues, redaction and security controls are preserved. No dependency/frontend changes or new field-name export filter are introduced. The optional legacy explicit-filter adapter is unchanged and omits the new signal; production uses the previously accepted default SDK diagnostic scopes.

## Retained verification

`TestCrawlerChallengeSignalWorkerSerialization` observes the actual worker processor's JSON, SDK HTTP alert and result encoder for all four signals, ID/class variants and deterministic first-match controls. Additional tests cover actual successful/nonchallenge processing, unknown metadata, legacy DTO compatibility and private canaries. The retained semantic detector and no-fallback controls run alongside these.

`TestWorkerChallengeSignalBoundedFailure` checks actual result decoding and exact operation binding. `TestFetchDiagnosticChallengeSignalSanitization` checks final scalar validation. `TestWebFetchChallengeSignalActualLogsAndAlert` exercises the tools callsite's actual local JSON and SDK HTTP serialization, privacy negatives and legacy filtering compatibility.

`TestMCPWebFetchServicePath` joins real MCP/billing/alert HTTP sockets, an owned disposable Redis and a separate worker test process. Its challenge case requires the same task's signal in encoded Redis, decoded DTO, local operation log and both actual SDK HTTP alerts. Other retained cases cover successful HTML/Markdown, ordinary quoted markers, render/receipt failures, billing denial, private admission and missing authentication. Chrome/public-peer dependencies and call-log persistence remain fixture seams; this does not attest the shipped production executable or a paid public request.

Both worker and consumer tests were written to compile against the unchanged source; the positive signal assertions fail at missing fields before implementation. Verification receipts bind subsequent test results to exact source trees/commits. An initial worker omission assertion matched a test filename in a safe stack trace; it was narrowed to the actual quoted JSON key, with the failed attempt retained. This was a test assertion defect, not a production classification defect.

## Release boundary

These changes are source-only Draft follow-ups. The earlier production rollout dispatch was rejected at 17:34 UTC for insufficient specific approval. Fresh explicit user approval at 19:28:33 UTC now authorizes diagnosis, fixes and release of this incident. Revised candidate qualification, independent review and fresh runtime/rollback preflight are required before rollout. Only the newly qualified incident candidates may proceed through coordinated release. Credentials, security settings, unrelated releases and blocked-target/paid-search replay remain outside this authorization. Earlier combined image qualifications apply only to their exact unchanged bytes; these new source revisions require integration and image qualification before a separately approved rollout. Rollback remains the recorded receipt-capable immutable pair, with producer before worker restoration on failed acceptance; no production action is performed by this document.
