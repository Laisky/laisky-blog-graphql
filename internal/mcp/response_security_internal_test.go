package mcp

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestMCPHTTPResponseSecurity keeps arbitrary tool text out of an HTML response
// context without corrupting JSON or SSE wire bytes.
func TestMCPHTTPResponseSecurity(t *testing.T) {
	const payload = `<script>alert("synthetic")</script>`
	for _, contentType := range []string{"", "text/html", "image/svg+xml", "application/json", "text/event-stream", "text/plain; charset=utf-8"} {
		for _, buffered := range []bool{false, true} {
			t.Run(contentType+map[bool]string{false: "/stream", true: "/buffered"}[buffered], func(t *testing.T) {
				response := httptest.NewRecorder()
				if buffered {
					captured := newCaptureResponseWriter()
					captured.Header().Set("Content-Type", contentType)
					captured.WriteHeader(http.StatusBadRequest)
					writeCapturedResponse(response, captured, []byte(payload))
				} else {
					writer := newLoggingResponseWriter(response, 4096)
					writer.Header().Set("Content-Type", contentType)
					writer.WriteHeader(http.StatusBadRequest)
					_, err := writer.Write([]byte(payload))
					require.NoError(t, err)
				}
				result := response.Result()
				require.NoError(t, result.Body.Close())
				require.Equal(t, http.StatusBadRequest, result.StatusCode)
				require.Equal(t, "nosniff", result.Header.Get("X-Content-Type-Options"))
				actualType := result.Header.Get("Content-Type")
				if contentType == "application/json" || contentType == "text/event-stream" {
					require.Equal(t, contentType, actualType)
				} else {
					require.Equal(t, "text/plain; charset=utf-8", actualType)
				}
				require.Equal(t, payload, response.Body.String(), "security policy must not rewrite protocol bytes")
			})
		}
	}
}

// TestMCPHTTPResponseSecurityWithoutLoggerAndOnFlush prevents logging settings
// and an early SSE flush from bypassing response MIME enforcement.
func TestMCPHTTPResponseSecurityWithoutLoggerAndOnFlush(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		_, err := w.Write([]byte("<svg/onload=alert(1)>"))
		require.NoError(t, err)
	})
	response := httptest.NewRecorder()
	withHTTPLogging(next, nil).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/mcp/", nil))
	require.Equal(t, "text/plain; charset=utf-8", response.Result().Header.Get("Content-Type"))
	require.Equal(t, "nosniff", response.Result().Header.Get("X-Content-Type-Options"))

	stream := httptest.NewRecorder()
	writer := newLoggingResponseWriter(stream, 4096)
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Flush()
	require.True(t, stream.Flushed)
	require.Equal(t, "nosniff", stream.Result().Header.Get("X-Content-Type-Options"))
	_, err := writer.Write([]byte("data: {\"content\":\"<script>\"}\n\n"))
	require.NoError(t, err)
	require.Equal(t, "data: {\"content\":\"<script>\"}\n\n", stream.Body.String())
}
