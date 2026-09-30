package mcp

import (
	"mime"
	"net/http"

	errors "github.com/Laisky/errors/v2"
)

// mcpResponseContentType restricts this protocol endpoint to non-HTML contexts.
// The wire bytes must not be HTML-escaped: JSON tool text and SSE frames have
// their own encoding. Unknown or missing types become inert UTF-8 plain text.
func mcpResponseContentType(header http.Header) string {
	mediaType, _, _ := mime.ParseMediaType(header.Get("Content-Type"))
	switch mediaType {
	case "application/json", "text/event-stream":
		return mediaType
	default:
		return "text/plain; charset=utf-8"
	}
}

// setMCPResponseHeaders also protects explicit status writes and early flushes.
func setMCPResponseHeaders(header http.Header) {
	header.Set("Content-Type", mcpResponseContentType(header))
	header.Set("X-Content-Type-Options", "nosniff")
}

// writeMCPResponse is the shared raw-wire boundary for captured and streamed
// responses. Declare the safe MIME context at the actual write, not only in
// callers, and preserve both protocol bytes and downstream write failures.
func writeMCPResponse(w http.ResponseWriter, body []byte) (int, error) {
	w.Header().Set("Content-Type", mcpResponseContentType(w.Header()))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	n, err := w.Write(body)
	return n, errors.WithStack(err)
}
