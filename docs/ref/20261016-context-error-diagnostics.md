# Context and error evidence references

Reviewed 2026-10-16 against the Go 1.27.2 toolchain used for qualification.

- [Go context documentation](https://pkg.go.dev/context@go1.27.2): a canceled context and a deadline-exceeded context are distinct observed states. Parent cancellation propagates only through derived contexts; independently queued worker contexts do not acquire caller provenance automatically. Sampling a context's state does not establish the primary cause of a separately returned error.
- [Go errors documentation](https://pkg.go.dev/errors@go1.27.2): wrapping and joining preserve multiple error identities. A sentinel match anywhere in a joined graph does not establish which cause occurred first. Diagnostics therefore retain existing error identities/codes and add a separately bounded composition observation.

The application traverses direct unwrap edges with an explicit budget for diagnostic composition. Narrow errorlint annotations explain why recursive matching at that step would bypass its budget. It does not inspect error text to infer lifecycle provenance.
