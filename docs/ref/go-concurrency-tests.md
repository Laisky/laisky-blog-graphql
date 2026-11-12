# Deterministic upload concurrency tests

The upload workers must not capture mutable named caller results. Snapshot metadata before spawning a worker and keep background errors local.

The cache and Telegram upload regressions use synchronous in-memory HTTP transports and MongoDB driver mock responses inside testing/synctest. With no blocking fixture operations, synctest.Wait drains runnable background work before final assertions, including all log entries. Waiting for only the first ERROR can miss a subsequent incorrect INFO.

References verified on 2026-11-12:

- https://pkg.go.dev/testing/synctest
- https://go.dev/doc/articles/race_detector
- https://go.dev/ref/mem

Race-enabled negative controls must report conflicting accesses in upload.go rather than configuration mutations in test fixtures.
