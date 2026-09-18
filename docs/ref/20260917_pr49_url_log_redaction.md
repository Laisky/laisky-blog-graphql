# PR #49: URL path-token log redaction

## Review disposition

- [x] Reproduce review comment `4040774653` against head `718c65389394093c4b99f8761e5ca67d668abe46` before changing production code.
- [x] Exclude a false positive: the synthetic URL passes the existing admission policy with deterministic public DNS results.
- [x] Fix `URLForLog` to remove both `Path` and `RawPath`, retaining only the HTTP(S) scheme and host, including an explicit port.
- [x] Preserve the failing tests as ordinary regression tests; add fuzz seeds and an origin-only invariant.
- [x] Execute the full shared-policy package tests, race detection, vet and a bounded fuzz run.
- [ ] Full Go 1.27 repository, real transport/database and frontend acceptance remain outstanding; this focused repair does not close the PR's G07 gap.

This is a confirmed defect, not a finding inferred solely from a string search.
The original source and existing test file were checked against GitHub blob IDs
`41cd3b5672b0e0ba7665268667c9bb96de4fba5e` and
`a2839d71aeae2fd7a3a3c996b1231d787da77c9b` before execution.

## Reproduction and contract

An admitted synthetic target such as
`https://example.com/reset/synthetic-path-token?token=synthetic-query#synthetic-fragment`
previously became `https://example.com/reset/synthetic-path-token` in the
sanitized URL field. The query and fragment were gone, but the path token remained,
including when that field was JSON-encoded for audit parameters.

The repaired log field is `https://example.com`. Explicit ports and IPv6 host
brackets are preserved. Malformed, relative, opaque and non-HTTP(S) inputs still
produce `[invalid URL]`. Redaction is idempotent. This function is for logging,
not for changing the actual fetch target, authorization, admission or billing.
MCP and GraphQL remain independent interfaces over shared functions.

`TestURLForLogOriginOnly` covers 20 cases: plain, escaped and double-escaped
path tokens; path parameters; Unicode; combined credentials/query/fragment;
IPv6 and ports; root/bare origins; empty query markers; whitespace/case;
and malformed, control-character, relative, opaque and unsupported URLs.
`TestURLForLogAllowedRequestAndAuditField` runs the real admission and logging
functions with synthetic input and deterministic DNS, then checks JSON encoding.
It is a field-level test, not a PostgreSQL call-log or application-logger test.
`FuzzURLForLogOriginOnly` requires every non-sentinel result to contain no
credentials, opaque value, path, query or fragment, and to be idempotent.
Its eight seeds run during ordinary `go test`; no automated fuzz or CI job is added.

## Execution evidence

Tests ran on the available Go 1.23.2 toolchain in an isolated copy of the complete
standard-library-only `toolpolicy` package, without changing or mocking its code.
The full repository requires Go 1.27 and dependencies unavailable in this environment.

| Check | Result |
| --- | --- |
| New regression tests against the unmodified reviewed source | FAIL: path disclosure reproduced, including an admitted request and serialized URL field |
| Same tests plus all existing package tests, `-race -shuffle=on -count=10` | PASS |
| `go vet` for the complete policy package | PASS |
| Package statement coverage / `URLForLog` coverage | 93.8% / 100.0% |
| Fuzz run, 15 seconds, two workers | PASS: 353,721 executions |

Local commands used `GO111MODULE=off GOTOOLCHAIN=local` inside the package-only
copy. No repository `go.mod`, toolchain setting or dependency was modified.
For a dependency-ready checkout, the normal command is:

```sh
go test -race -shuffle=on -count=10 -cover ./internal/library/toolpolicy
go vet ./internal/library/toolpolicy
go test ./internal/library/toolpolicy -run '^$' -fuzz '^FuzzURLForLogOriginOnly$' -fuzztime=15s -parallel=2
```

The reviewed helper is used by MCP fetch URL logging, the GraphQL WebFetch URL
log/audit field, and the shared renderer's URL log field. Source tracing confirms
those call sites; end-to-end logging and database persistence were not executed.
This change does not retroactively scrub stored records or establish that arbitrary
provider error strings, response bodies, hosts, or unrelated audit parameters are
free of secrets. Do not generalize the helper's test coverage to the whole system.

## Primary references

- [Go `net/url.URL`](https://pkg.go.dev/net/url#URL): `Path` and optional `RawPath` both represent path data; URL string serialization is not secret redaction.
- [CWE-532](https://cwe.mitre.org/data/definitions/532.html): sensitive information must not be introduced into logs.

No CI configuration, benchmark trigger, dependency, database migration, frontend,
GraphQL schema, registration switch, or generated source changes are part of this repair.
