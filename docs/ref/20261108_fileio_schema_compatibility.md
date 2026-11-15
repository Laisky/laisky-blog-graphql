# FileIO mutation schema compatibility

Observed November 8, 2026: a native tool declaration for both file_write and
file_restore_version exposed only the create_only union, although find_tool
returned all arguments. The previous valid JSON Schema factored common fields
at the root and used condition-only oneOf branches. A branch-oriented client
could therefore omit project/path/content or history_id/expected_version.

The fix retains the root contract and makes each union branch a complete object
with common properties and required fields. Creation still forbids a version;
edits require the opaque version and allow create_only only omitted or false.
Each alternative declares exactly the arguments it accepts, so the create
alternative omits expected_version (its `not` clause still rejects it).
Property descriptions/types/patterns and runtime precondition enforcement remain
unchanged. No new operation, credential, storage format or deployment is involved.

## Client probes

Method: a dependency-free stdio MCP server served the serialized schemas, the
client listed or called the tools, and the server logged each `tools/call`.
The logged arguments were replayed through mcp-go's input validator.

**Codex CLI 0.169.0 (code mode), November 15, 2026 — the affected client.**
Codex renders each MCP tool as a TypeScript `exec tool declaration`. Its
`ALL_TOOLS` description was dumped verbatim. For a root object with `oneOf`,
it renders only the alternatives as a TS union. It ignores sibling root
`properties`/`required` and drops `not`.

| Schema | Rendered `args` type | Calls issued for create / edit / restore-absent / restore-live |
| --- | --- | --- |
| master (condition-only alternatives) | `{ create_only?: false; } \| { create_only: true; }` | `{create_only:true}`, `{create_only:false}` (all arguments lost; validator rejects all four) |
| complete alternatives, create keeps `expected_version` | create alternative shows `expected_version?: string` although `not` forbids it | all four correct and accepted |
| complete alternatives, create omits `expected_version` (shipped) | edit: `expected_version: string; create_only?: false`; create: `create_only: true`, no version | all four correct and accepted |

A boolean `false` property schema rendered as `expected_version?: string`, and
`{"not": {}}` as `expected_version?: unknown`, so omitting the forbidden
property is the only form that renders the create alternative accurately.

**Claude Code.** Checked November 13 (stdio probe) and November 15 (the
deployed master schema, and an end-to-end probe of the shipped schema on Claude
Code 2.1.325 with Sonnet 5.5). Claude Code flattens root properties, drops `oneOf`,
and adds a description line built from each alternative's `required` list. For
example: "Provide parameters for exactly one of: (expected_version) or
(create_only)". With complete alternatives the line lists full required sets,
such as (project, path, content, expected_version). The declared parameters
are unchanged. The four end-to-end calls were correct and accepted.

## Wider client matrix (research, November 15, 2026)

| Client | Root `oneOf` handling | Source |
| --- | --- | --- |
| Codex CLI code mode | Renders only the alternatives (`render_map` returns before `properties`) | openai/codex `code-mode-protocol/src/json_schema_types.rs`, plus the probe above |
| Codex CLI Responses path | Keeps root `oneOf` beside `properties` and sends `strict: false`. It drops `not`, `if`/`then`/`else`, `dependent*` and `pattern`. A typeless `{"const": false}` becomes a string enum, so alternatives keep `type: boolean` | openai/codex `tools/src/json_schema.rs`, `responses_api.rs` |
| Claude Code 2.1.325 | Flattens root and alternative properties and keeps the root `required`. Adds the "exactly one of" description line | binary and session observation |
| VS Code Copilot | Deletes root `oneOf`/`anyOf`/`allOf`/`not`/`if`/`then`/`else` and keeps root properties | microsoft/vscode `extensions/copilot/src/extension/tools/common/toolSchemaNormalizer.ts` |
| Gemini CLI | Passes the schema as `parametersJsonSchema` and validates arguments locally with Ajv | google-gemini/gemini-cli `mcp-client.ts` |
| Anthropic Messages API, raw | Rejects root `oneOf`/`anyOf`/`allOf` with HTTP 400. This is not in the tool docs | third-party test data, [olaservo research hub](https://github.com/olaservo/research-hub-public/tree/main/mcp/tool-schemas) |
| OpenAI Chat Completions, raw | Rejects root `oneOf`, including non-strict. The Responses API accepts it non-strict | same test data; [structured outputs](https://developers.openai.com/api/docs/guides/structured-outputs.md) |

The [MCP 2026-07-28 tools spec](https://modelcontextprotocol.io/specification/2026-07-28/server/tools)
and SEP-2106 allow any 2020-12 keyword at the root. Servers must validate tool
inputs, and nothing in the spec says to avoid root composition.

**Remaining portability gap (pre-existing, outside this fix).** The root `oneOf`
dates from before this change. A client that forwards MCP schemas to the
Anthropic Messages API or OpenAI Chat Completions without normalizing them gets
a 400, and the failure takes down the whole tool list. The most portable form is
a flat root: `properties` plus common `required`, with the either/or rule in
the descriptions and enforced by the server, as it already is. That form gives
up the schema-level exactly-one constraint that `docs/manual/fileio_versioning.md`
documents. A root
`if`/`then`/`else` keeps that constraint without a union. Its acceptance is
contested: VS Code strips it with a "fails for claude and 4o" comment, while the
2026 test data shows Anthropic accepting it. Changing the published
representation is therefore a separate owner decision and needs probes against
the raw APIs first.

## Discovery impact

`find_tool` BM25 and regex search over the full serialized schema.
`rag.Tokenize` returns unique tokens, so term frequency is 0/1 and document
length is the unique-token count. Complete alternatives add no tokens: file_write
has 109 unique tokens before and after. Over a 20-tool catalog built from the
server card, 15 probe queries ranked identically, and per-query BM25 time stayed
within noise (about 0.7-1.0 ms). Only the embedding document grows, from about
1.7 KB to 4.1 KB for each of the two tools, once per lazily built index.
`TestFileMutationUnionsKeepLexicalDiscovery` guards the token-set equality.

## Static server card

The card is a compact, hand-curated catalog. It may omit descriptions, and it
advertises stricter canonical values: `content_encoding` and `mode` enums, and
an integer, non-negative `offset`. The contract test allows exactly those
stricter keywords. It requires identical argument names, required sets,
conditions and per-alternative argument sets, so any other drift fails.

Primary references consulted:
- [JSON Schema composition](https://json-schema.org/understanding-json-schema/reference/combining):
  factoring sibling constraints is valid; oneOf means exactly one matching branch.
- [MCP tool schemas](https://github.com/modelcontextprotocol/modelcontextprotocol/blob/main/docs/specification/2026-07-28/server/tools.mdx):
  inputSchema is JSON Schema, with 2020-12 as the default dialect.
- mcp-go v1.1.1 server input validation: the local acceptance tests use its real
  schema validator and serialized tools/list boundary.

Regression scope: serialized tools/list and find_tool argument discoverability;
standalone-alternative and full-schema condition acceptance/refusal; each
alternative's declared arguments matching what it accepts; lexical discovery
equality; a flat-reader control; static card drift; and concrete RAG/PageIndex
mutation/refusal tests. Native-client probes need a paid model call and stay
deliberate manual acceptance. A deployed client sees the new declaration only
after release and a refreshed tool catalog.
