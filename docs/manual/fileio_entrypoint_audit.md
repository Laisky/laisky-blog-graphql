# FileIO entrypoint audit

Date: 2026-09-17. Audited repository: `Laisky/laisky-blog-graphql`.
Base: PR #46 at `7eb2449c0dc32eeee406795a0fb2bb3cab38387e`.

## Actual entrypoints

| Surface | Actual route and implementation | Concurrency boundary |
| --- | --- | --- |
| MCP | `tools/list`, progressive `find_tool`, `tools/call` to FileIO handlers; `mcp_pipe` invokes the same registered handlers | `RequireClientFilePreconditions` -> version-capable project plugin -> storage transaction; create-only or exact read token required for every public mutation |
| GraphQL | `/query`, `/query/`, `/query/v2`, `/query/v2/`, with configured URL prefixes, share the generated executable schema | No FileIO field or FileIO resolver exists; unknown fields must fail validation rather than reach storage |
| Web forms | FileIO page -> `callFileTool` -> MCP | Same MCP preconditions; independent source/destination observations for rename |
| Web editor/history | FileIO page -> `callFileAPI` -> authenticated FileIO HTTP handler | Strong `If-Match` or create-only `If-None-Match`; original token preserved when delegating to the selected plugin |
| Static discovery | Public server card, `llms.txt`, `llms-full.txt` | Documentation and representative schemas must not advertise unguarded writes or directory mutations |

All six schemas configured in `gqlgen.yml` were inspected, along with generated
schema wiring and resolver construction. `ResolverArgs.FilesService` is used to
mount the dedicated HTTP API; its presence does not create a GraphQL FileIO
resolver. `ExtractKeyInfo`, Arweave uploads and blog post history are distinct
features. The `graphql` value used for the FileIO project is a namespace, not a
transport. This audit does not introduce a new GraphQL API.

## Findings and changes in this patch

### ENTRY-001: progressive MCP discovery discarded raw schemas

`file_write` uses `RawInputSchema` to express the mandatory alternatives with
`oneOf`, clearing its structured `InputSchema`. `find_tool` previously returned
only the cleared field and excluded raw-schema parameters from its search text.
Its response could therefore differ from the live `tools/list` definition.

Both paths now use the canonical `mcp.Tool` JSON marshaler. Malformed/conflicting
schema representations fail instead of becoming a permissive fallback. The old
plugin-field test now inspects the serialized schema. The same audit moves a
`len(t.tools)` read under the existing mutex in `buildResponse`.

### ENTRY-002: HTTP save/restore bypassed the project plugin

The old HTTP handler called the raw FileIO Service while MCP called the selected
RAG/PageIndex plugin. This was **not an unguarded content-write bypass**: Service
CAS still applied. However, editor saves/restores did not run PageIndex's own
write/index/summary pipeline and could disagree with MCP's plugin behavior.

Production HTTP wiring now resolves the same version-aware project plugin as
MCP. Unsupported/nil/failed resolution does not fall back to raw storage. HTTP
restore reads immutable tenant-scoped historical bytes and rebinds the ORIGINAL
live precondition to the final plugin write. It never fetches a fresh token.
A storage-only handler without an explicit resolver is retained for standalone
storage use/tests and still enforces conditions; the production web mount always
supplies the resolver.

The deterministic PageIndex cross-entrypoint tests use `.txt` files to inspect
`skip_rag_index` and the published complete-content summary hash without provider
calls. They do not claim to validate long-document reasoning quality or zero-second
index freshness. PageIndex work can fail after its content transaction commits;
only precondition rejection is guaranteed to have no mutation effects. No automatic
retry or exactly-once success replay is added.

### ENTRY-003: static discovery retained the old contract

The public server card omitted required edit conditions and still advertised
recursive directory deletion/moves. Update its FileIO schemas and descriptions,
retain create-only versus expected-version alternatives, and add a runtime/card
contract regression. Public agent guides now describe the same edit protocol and
clarify that the GraphQL endpoint is not a FileIO transport.

### ENTRY-004: historical IDs could lose precision in the browser

Live version tokens were already strings, but historical row IDs were still JSON
numbers and passed through `Number(...)` in the history selector. Adjacent IDs
`9007199254740992` and `9007199254740993` alias when represented as JavaScript
numbers. That is a latent incorrect-history-selection risk; no claim is made that
production IDs have reached that range.

Return historical IDs as decimal strings, validate them separately from live
CAS tokens, and carry them unchanged in preview/restore URLs. Tests cover adjacent
large IDs and the exact selected historical bytes. Existing GraphQL IDs are not
changed: there is no GraphQL FileIO history field.

### ENTRY-005: optional metadata errors misreported successful content reads

A failed `file_stat` previously entered the same catch as `file_read`, despite a
successful content/version snapshot. Isolate the optional metadata failure into a
separate warning. It never replaces the read token, disables a valid edit, or
installs a late warning into another selected file.

## Regression coverage added

| Test group | Behavior checked |
| --- | --- |
| `TestFindToolFileIOSchemaMatchesToolsList` and related tests | Raw/structured discovery equality, parameter indexing, malformed schemas, concurrent definition replacement |
| `TestStaticServerCardKeepsFileIOConditions` | Static mutation requirements and `file_write.oneOf` stay aligned with runtime definitions |
| `TestGraphQLFileIOBoundary` | Generated schema and actual GraphQL HTTP validation reject unsupported FileIO fields, aliases and fragments before resolver execution |
| `TestFileIOCrossEntrypointConditions` | MCP -> HTTP and HTTP -> MCP stale bases; original-token restore; missing conditions; content/hash/history/outbox invariants; real RAG/PageIndex project routing |
| `TestFileIOPostgresMCPAndHTTPContendOnOneVersion` | Independent connection pools, exactly one concurrent writer wins, other transport returns a conflict |
| `TestFileIOHTTPResolverFailureNeverFallsBackToStorage` | Unavailable configured writer cannot produce a raw-storage write |
| `TestFileIOMCPPipeRetainsVersionConditions` | Referenced large string token survives pipeline substitution; stale and missing conditions remain rejected |
| `TestHTTPHistoryIDsRoundTripWithoutJSONNumberRounding` | Adjacent large IDs stay distinct through list/content/restore |
| Frontend history ID and page regressions | Exact history URLs, original `If-Match`, independent metadata errors and late-response suppression |

The tests use real storage/handlers where stated, mocked browser transports for
React behavior, and no new CI job. PostgreSQL cases explicitly skip without a
disposable `FILEIO_TEST_POSTGRES_DSN`. Skips are not PostgreSQL passes.

## Validation status

This patch was prepared without a complete repository dependency environment.
The actual completed checks are recorded with its accompanying validation logs:

- Strict TypeScript checking of the patched `version-state.ts` with TypeScript 5.8.3.
  Its original source was verified against the Git blob identity before patching.
- 25 native Node tests against that compiled production module, repeated three times.
  These are protocol tests, not React/Vitest application tests.
- 26 public-schema-fragment cases using a JSON Schema validator. The old fragment's
  missing-condition acceptance is reproduced; the patched fragment rejects it.
- `gofmt` syntax/format checks for the nine newly added Go files.

Not executed: repository Go tests/type checking, PostgreSQL integration tests,
generated GraphQL HTTP tests, the React/Vitest component tests, full frontend
build, or authenticated production actions. Test source presence is not execution
evidence. Earlier PR #45/#46 results do not validate these new changes.

Required acceptance in a dependency-ready checkout:

```sh
go test -race ./internal/mcp/files ./internal/mcp/tools ./internal/mcp ./internal/web
go vet ./internal/mcp/files ./internal/mcp/tools ./internal/web
# Set FILEIO_TEST_POSTGRES_DSN to a disposable database with pgvector, then:
go test -race -shuffle=on -count=3 ./internal/mcp/files -run '^TestFileIO'
cd web
pnpm install --frozen-lockfile
pnpm test -- src/features/mcp/file-io
pnpm build
```

No workflow, benchmark trigger, dependency, lockfile, schema migration or generated
GraphQL source is changed by this audit patch. No benchmark, merge, deployment or
production-data operation was performed. The patch's publication status must be
reported separately from its implementation and test status.
