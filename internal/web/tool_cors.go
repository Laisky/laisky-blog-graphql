package web

import "net/http"

const toolCORSRequestHeaders = "Authorization, Content-Type, Accept, If-Match, If-None-Match, Mcp-Session-Id, MCP-Protocol-Version, Last-Event-ID, Cache-Control, Pragma"
const toolCORSResponseHeaders = "ETag, Mcp-Session-Id, MCP-Protocol-Version, Retry-After"

// setToolCORSHeaders is called only after the existing origin allowlist decision.
// A wildcard does not authorize Authorization or credentialed custom headers.
func setToolCORSHeaders(header http.Header) {
	header.Set("Access-Control-Allow-Headers", toolCORSRequestHeaders)
	header.Set("Access-Control-Expose-Headers", toolCORSResponseHeaders)
}
