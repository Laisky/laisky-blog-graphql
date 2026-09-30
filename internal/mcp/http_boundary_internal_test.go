package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	glog "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/Laisky/zap/zapcore"
	"github.com/Laisky/zap/zaptest/observer"
	"github.com/stretchr/testify/require"
)

// TestMCPHTTPLogSecurity keeps all URL credentials out of both request and
// response diagnostics without changing the credentials delivered to handlers.
func TestMCPHTTPLogSecurity(t *testing.T) {
	const secret = "synthetic-mcp-query-key+private"
	for _, alias := range []string{"APIKEY", "apikey", "api_key"} {
		t.Run(alias, func(t *testing.T) {
			core, observed := observer.New(zap.DebugLevel)
			logger, err := glog.NewWithName("http-boundary", glog.LevelDebug,
				zap.WrapCore(func(zapcore.Core) zapcore.Core { return core }))
			require.NoError(t, err)
			rawURL := "http://user:synthetic-url-password@example.test/mcp/?" + alias + "=" + url.QueryEscape(secret)
			request := httptest.NewRequest(http.MethodPost, rawURL, strings.NewReader(`{"method":"tools/list"}`))
			request.Header.Set("Authorization", "Bearer synthetic-header-key")
			called := false
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				require.Equal(t, secret, r.URL.Query().Get(alias))
				require.Equal(t, "Bearer synthetic-header-key", r.Header.Get("Authorization"))
				w.Header().Set("Content-Type", "application/json")
				_, writeErr := w.Write([]byte(`{"result":{"tools":[]}}`))
				require.NoError(t, writeErr)
			})
			response := httptest.NewRecorder()
			withHTTPLogging(handler, logger).ServeHTTP(response, request)
			require.True(t, called)
			require.Equal(t, http.StatusOK, response.Code)
			require.Equal(t, rawURL, request.URL.String(), "redaction must not mutate the request URL")
			logged, err := json.Marshal(observed.All())
			require.NoError(t, err)
			for _, forbidden := range []string{secret, url.QueryEscape(secret), "synthetic-url-password", "synthetic-header-key"} {
				require.NotContains(t, string(logged), forbidden)
			}
			require.NotEmpty(t, observed.FilterMessage("incoming http request").All())
			require.NotEmpty(t, observed.FilterMessage("outgoing http response").All())
		})
	}
}

// TestMCPHTTPLogBodyBudget verifies a fixed-size logging prefix, byte-for-byte
// downstream replay, and ownership of Close, including its error.
func TestMCPHTTPLogBodyBudget(t *testing.T) {
	payload := strings.Repeat("synthetic-body-", httpLogBodyLimit)
	source := &httpBoundaryBody{reader: strings.NewReader(payload), closeErr: io.ErrClosedPipe}
	request := httptest.NewRequest(http.MethodPost, "/mcp/", nil)
	request.Body = source
	prefix, truncated, err := readAndRestoreRequestBody(request, 32)
	require.NoError(t, err)
	require.True(t, truncated)
	require.Equal(t, payload[:32], prefix)
	require.Equal(t, 33, source.readBytes, "logging must not pre-read the entire body")
	require.Zero(t, source.closes, "the consumer still owns the request body")
	body, err := io.ReadAll(request.Body)
	require.NoError(t, err)
	require.Equal(t, payload, string(body))
	require.ErrorIs(t, request.Body.Close(), io.ErrClosedPipe)
	require.Equal(t, 1, source.closes)
}

// TestMCPHTTPLogReadFailureIsReplayed prevents logging from consuming and
// hiding a truncated request's terminal read error from the actual handler.
func TestMCPHTTPLogReadFailureIsReplayed(t *testing.T) {
	source := &httpBoundaryBody{reader: &httpBoundaryFailedReader{}}
	request := httptest.NewRequest(http.MethodPost, "/mcp/", nil)
	request.Body = source
	_, _, err := readAndRestoreRequestBody(request, 32)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	body, err := io.ReadAll(request.Body)
	require.Equal(t, "partial", string(body))
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	require.NoError(t, request.Body.Close())
	require.Equal(t, 1, source.closes)
}

type httpBoundaryBody struct {
	reader    io.Reader
	readBytes int
	closes    int
	closeErr  error
}

func (b *httpBoundaryBody) Read(p []byte) (int, error) {
	n, err := b.reader.Read(p)
	b.readBytes += n
	return n, err
}

func (b *httpBoundaryBody) Close() error {
	b.closes++
	return b.closeErr
}

type httpBoundaryFailedReader struct {
	done bool
}

func (r *httpBoundaryFailedReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	r.done = true
	return copy(p, "partial"), io.ErrUnexpectedEOF
}

// TestMCPHTTPResponseSecurityWire validates actual committed HTTP headers,
// including implicit WriteHeader and a flushed SSE prefix, not just map values.
func TestMCPHTTPResponseSecurityWire(t *testing.T) {
	const script = `<script>alert("synthetic")</script>`
	for _, contentType := range []string{"", "text/html", "application/json", "text/event-stream"} {
		for _, buffered := range []bool{false, true} {
			t.Run(contentType+map[bool]string{false: "/stream", true: "/buffered"}[buffered], func(t *testing.T) {
				handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					payload := []byte(r.URL.Query().Get("value"))
					if buffered {
						captured := newCaptureResponseWriter()
						captured.Header().Set("Content-Type", contentType)
						writeCapturedResponse(w, captured, payload)
						return
					}
					writer := newLoggingResponseWriter(w, 32)
					writer.Header().Set("Content-Type", contentType)
					if contentType == "text/event-stream" {
						writer.Flush()
					}
					_, _ = writer.Write(payload)
				})
				server := httptest.NewServer(handler)
				defer server.Close()
				request, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
					server.URL+"/?value="+url.QueryEscape(script), nil)
				require.NoError(t, err)
				response, err := server.Client().Do(request)
				require.NoError(t, err)
				body, readErr := io.ReadAll(response.Body)
				closeErr := response.Body.Close()
				require.NoError(t, readErr)
				require.NoError(t, closeErr)
				require.Equal(t, http.StatusOK, response.StatusCode)
				require.True(t, bytes.Equal([]byte(script), body), "wire bytes must not be HTML-escaped")
				expected := "text/plain; charset=utf-8"
				if contentType == "application/json" || contentType == "text/event-stream" {
					expected = contentType
				}
				require.Equal(t, []string{expected}, response.Header.Values("Content-Type"))
				require.Equal(t, "nosniff", response.Header.Get("X-Content-Type-Options"))
			})
		}
	}
}
