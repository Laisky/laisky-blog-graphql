# MCP Inspector browser package

GraphQL embeds the Inspector 1.0.6 browser app in its own same-origin page.
The registry package prebuild includes Zod 3, affected by
[GHSA-mrhx-c7xc-m3w5](https://github.com/advisories/GHSA-mrhx-c7xc-m3w5).
A pnpm override cannot patch prebuilt bytes. This archive rebuilds the
[original UI source](https://github.com/modelcontextprotocol/inspector/tree/2997156791354497acb66091c79a2409d7f14429)
with audited dependencies, including Zod 4.6.7 and MCP SDK 1.32.2. There
are no source edits. It adds no Node service, proxy, or runtime download.
The upstream licensing terms are included in the original distribution layout.

To verify the checked-in archive, run `bash check-rebuild.sh` from this
directory with Node 22.22.3 and npm 10.9.8. The script verifies the pinned
source archive checksum, installs the locked graph without lifecycle
hooks, audits the client's production dependencies, builds the client,
and compares the resulting archive byte for byte. The original client
manifest remains in the archive so the application dependency audit
continues to see the browser package's runtime dependencies.

Return to the registry package when an official browser-only release
ships rebuilt and audited assets. Recheck the direct MCP connection,
credential scope, and reconnect behavior before dropping this package.
