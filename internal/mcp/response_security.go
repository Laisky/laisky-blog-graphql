package mcp

import (
	"mime"
	"net/http"
)

// setMCPResponseHeaders restricts this protocol endpoint to non-HTML contexts.
// The wire bytes must not be HTML-escaped: JSON tool text and SSE frames have
// their own encoding. Unknown or missing types become inert UTF-8 plain text.
func setMCPResponseHeaders(header http.Header) {
	mediaType, _, _ := mime.ParseMediaType(header.Get("Content-Type"))
	switch mediaType {
	case "application/json":
		header.Set("Content-Type", "application/json")
	case "text/event-stream":
		header.Set("Content-Type", "text/event-stream")
	default:
		header.Set("Content-Type", "text/plain; charset=utf-8")
	}
	header.Set("X-Content-Type-Options", "nosniff")
}
