# FileIO mutation schema compatibility

Observed November 8, 2026: a native tool declaration for both file_write and
file_restore_version exposed only the create_only union, although find_tool
returned all arguments. The previous valid JSON Schema factored common fields
at the root and used condition-only oneOf branches. A branch-oriented client
could therefore omit project/path/content or history_id/expected_version.

The fix retains the root contract and makes each union branch a complete object
with common properties and required fields. Creation still forbids a version;
edits require the opaque version and allow create_only only omitted or false.
Property descriptions/types/patterns and runtime precondition enforcement remain
unchanged. No new operation, credential, storage format or deployment is involved.

Primary references consulted:
- [JSON Schema composition](https://json-schema.org/understanding-json-schema/reference/combining):
  factoring sibling constraints is valid; oneOf means exactly one matching branch.
- [MCP tool schemas](https://github.com/modelcontextprotocol/modelcontextprotocol/blob/main/docs/specification/2026-07-28/server/tools.mdx):
  inputSchema is JSON Schema, with 2020-12 as the default dialect.
- mcp-go v1.1.1 server input validation: the local acceptance tests use its real
  schema validator and serialized tools/list boundary.

Regression scope: serialized tools/list and find_tool argument discoverability,
standalone branch and full-schema condition acceptance/refusal, a flat-reader
control, and concrete RAG/PageIndex mutation/refusal tests. This reproduces the
compatibility mechanism, not the internals of an unavailable native converter.
Deployed native declaration acceptance requires release and a refreshed client
catalog; local tests alone do not establish that rollout.
