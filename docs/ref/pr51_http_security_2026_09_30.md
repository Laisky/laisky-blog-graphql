# PR #51 HTTP security acceptance — 2026-09-30 UTC

## Source and decision

Accepted runtime: `eff47d5e1cc1db5cb664cb789ddeba738b4b840c`.
Retain the original JSON-RPC/SSE wire format, request credentials, per-request
identity, TLS verification and legitimate public image access. Enforce safety
at the network and response-write boundaries rather than disabling scanners,
escaping complete protocol frames or replacing failures with successful results.

## Regression matrix

| Boundary | Negative/positive controls and outcome |
| --- | --- |
| Image destination | Private, shared/metadata, loopback, link-local, reserved/documentation, mapped IPv6, transition/NAT64 and scoped addresses cannot be dialed; ordinary public destinations remain available through a synthetic resolver. |
| DNS and redirects | Resolve a logical origin once per hop and connect to the validated numeric authority. Reject mixed public/private DNS answers and disallowed redirects before the next dial. Resolve relative redirects against the logical origin, not the numeric connection URL. |
| TLS and HTTP identity | Preserve logical Host and TLS ServerName, require certificate verification and TLS 1.2 minimum. Positive HTTPS and negative untrusted-certificate controls remain distinct. Environment proxies cannot bypass destination validation. |
| Deadlines and resource limits | One context bounds DNS, redirects and response reading. Preserve cancellation and size-error identities, cap bytes, close response bodies, and do not disclose URL query values through fetch errors. |
| Response MIME type | Real HTTP buffered and streamed responses retain exact bytes with JSON/SSE MIME types; unknown, missing or HTML MIME types become UTF-8 plain text. Every path commits `nosniff`, including an SSE flush before body bytes. |
| Credential logging | The three legacy query aliases and URL userinfo never appear raw or URL-encoded in incoming/outgoing logs. The original URL and Authorization passed to the handler are unchanged. |
| Logging allocation | A 32-byte test budget reads only 33 source bytes; the production 4096-byte budget reads at most 4097. Downstream receives all bytes. This is not a global request-size cap. |
| Body lifecycle/errors | Logging does not close the body; the downstream owner receives the original Close error. Partial data plus `io.ErrUnexpectedEOF` are replayed to the handler rather than consumed by logging. |

The image tests never dial real private/metadata endpoints. A trusted test dialer
maps approved numeric destinations to local fixtures; production uses the normal
numeric-address dialer. The trusted hook is not configurable by remote callers.

## Red-to-green evidence

The tests-only commit `dae48053c19f180ee1e9907e82091c8583fb9bfc` deliberately fails
[run 36649626987](https://github.com/Laisky/laisky-blog-graphql/actions/runs/36649626987):
`TestMCPHTTPLogSecurity` leaks the escaped synthetic credential for all three
aliases, `TestMCPHTTPLogBodyBudget` sees premature Close, and
`TestMCPHTTPLogReadFailureIsReplayed` loses the partial bytes. Other protocol,
session, build/vet and generation steps pass. The real HTTP MIME test is a
positive control and already passed; it does not falsely label every raw write
as exploitable XSS.

`eff47d5` repairs the logging defects and centralizes the non-HTML response-write
policy. [Network/security run 36650150958](https://github.com/Laisky/laisky-blog-graphql/actions/runs/36650150958)
passes three race-enabled repetitions of the network, MIME and logging tests.
[Full compatibility run 36650150642](https://github.com/Laisky/laisky-blog-graphql/actions/runs/36650150642)
passes the entire Go race/coverage suite, 20 session regressions, build, vet,
module locks, owner/pure-Go gates and clean GraphQL regeneration.

The previous complete CodeQL Go analysis contained two reflected-XSS results at
separate raw-write sinks. The accepted implementation makes the response context
explicit on the same writer at the shared write boundary. Exact-head Go analysis
`1863382483`, JavaScript/TypeScript analysis `1863374525`, and Actions analysis
`1863373765` each contain zero results and no analysis errors. Both PR head and
merge open-alert queries are empty in the [complete evidence collection](https://github.com/Laisky/laisky-blog-graphql/actions/runs/36650598166).
No alert dismissals, query exclusions, custom taint barriers or HTML-escaping of
MCP wire bytes were used.

## Durable evidence and limits

The complete evidence ZIP is artifact `11070301907`, SHA-256
`1d224953ef5461b0e6cddb59d0b40e654b2d026b764cd3b6706a3566e223fe82`.
It records the actual accepted runtime SHA, scanner categories, complete SARIF,
check annotations and open-alert queries. The temporary read-only collector is
removed; `mcp-network-security` remains a PR gate. Final cleanup-head results are
recorded in the PR discussion rather than described as already completed here.

The checks cover these boundaries, not arbitrary undiscovered defects or an
unlimited audit of every future configuration. No production endpoint probing,
paid provider request, deployment, merge or data migration is part of acceptance.
For the dependency/module advisory classification and the wider 353-to-zero
cleanup, see [project quality closure](pr51_project_quality_2026_09_29.md).

## Primary references

- [OWASP SSRF prevention](https://cheatsheetseries.owasp.org/cheatsheets/Server_Side_Request_Forgery_Prevention_Cheat_Sheet.html)
- [CodeQL Go reflected-XSS query](https://codeql.github.com/codeql-query-help/go/go-reflected-xss/)
- [CodeQL MIME-context model](https://github.com/github/codeql/blob/main/go/ql/lib/semmle/go/security/Xss.qll)
- [Pinned MCP transport implementation](https://github.com/mark3labs/mcp-go/blob/v1.1.1/server/streamable_http.go)
