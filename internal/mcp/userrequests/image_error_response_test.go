package userrequests

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	errs "github.com/Laisky/errors/v2"
	"github.com/stretchr/testify/require"
)

// imageErrorBody decodes an image error response.
func imageErrorBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), "body=%s", rec.Body.String())
	return body
}

// requireNoInternals fails when message carries any of the internal details
// a storage or transport error wraps.
func requireNoInternals(t *testing.T, message string, internals ...string) {
	t.Helper()
	require.NotEmpty(t, message)
	for _, detail := range internals {
		require.NotContains(t, message, detail, "the client message leaks an internal detail")
	}
}

// TestImageServerErrorsHideInternalDetails is the PR #94 review repro
// "internal storage errors can reach HTTP clients": writeImageError returned
// err.Error() for every status, so a failed PUT exposed the object key, the
// storage operation and the backend address to the caller. A 5xx response now
// carries a stable message per code (the detail stays in the server log),
// keeps the code and the attachment index, and validated client-input errors
// (4xx) keep their descriptive message.
func TestImageServerErrorsHideInternalDetails(t *testing.T) {
	t.Run("storage failure", func(t *testing.T) {
		h := newOrphanHarness(t, defaultImageSettings(), "")
		h.store.BeforePut = func(_ context.Context, key string) error {
			return errs.Errorf("storage: put object %q: Put \"http://10.1.2.3:9000/bucket/%s\": dial tcp 10.1.2.3:9000: connect: connection refused", key, key)
		}
		rec := h.post(t, context.Background(), "sk-error-leak-storage-1234", testPNG(t, 40, 30))
		require.Equal(t, http.StatusServiceUnavailable, rec.Code, "body=%s", rec.Body.String())
		body := imageErrorBody(t, rec)
		require.Equal(t, "storage_unavailable", body["error"])
		message, _ := body["message"].(string)
		requireNoInternals(t, message, "10.1.2.3", "dial tcp", "storage:", "mcp/images", "bucket", ".png")
		require.Equal(t, publicImageErrorMessage("storage_unavailable"), message)
		require.EqualValues(t, 0, body["attachment_index"], "the failing attachment is still identified")
	})

	t.Run("limiter refuses an ended request", func(t *testing.T) {
		h := newOrphanHarness(t, defaultImageSettings(), "")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		// The limiter admits nothing for an ended request: image_busy.
		rec := h.post(t, ctx, "sk-error-leak-busy-123456", testPNG(t, 40, 30))
		require.Equal(t, http.StatusServiceUnavailable, rec.Code, "body=%s", rec.Body.String())
		body := imageErrorBody(t, rec)
		require.Equal(t, "image_busy", body["error"])
		message, _ := body["message"].(string)
		requireNoInternals(t, message, "context canceled", "in flight")
		require.Equal(t, publicImageErrorMessage("image_busy"), message)
		require.Equal(t, "5", rec.Header().Get("Retry-After"))
	})

	t.Run("body read deadline", func(t *testing.T) {
		prev := createRequestTimeout
		createRequestTimeout = 300 * time.Millisecond
		t.Cleanup(func() { createRequestTimeout = prev })
		h := newOrphanHarness(t, defaultImageSettings(), "")
		srv := httptest.NewServer(h.handler)
		t.Cleanup(srv.Close)
		raw, contentType := multipartBody(t, testPNG(t, 40, 30))
		pr, pw := io.Pipe()
		t.Cleanup(func() { _ = pw.Close() })
		req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/requests", pr)
		require.NoError(t, err)
		req.Header.Set("Content-Type", contentType)
		req.Header.Set("Authorization", "Bearer sk-error-leak-deadline-1234")
		go func() { _, _ = pw.Write(raw.Bytes()[:64]) }() // a partial first part, then silence
		resp, err := srv.Client().Do(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
		var body map[string]any
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.Equal(t, "image_busy", body["error"])
		message, _ := body["message"].(string)
		requireNoInternals(t, message, "i/o timeout", "read tcp", "127.0.0.1", "deadline")
		require.Equal(t, publicImageErrorMessage("image_busy"), message)
	})

	t.Run("body spool failure is a server error", func(t *testing.T) {
		h := newOrphanHarness(t, defaultImageSettings(), "")
		t.Setenv("TMPDIR", "/nonexistent-review-tmp/server-secret-dir")
		// A part larger than the in-memory multipart buffer is spooled to a
		// temporary file, which cannot be created.
		rec := h.post(t, context.Background(), "sk-error-leak-spool-1234", bytes.Repeat([]byte{'x'}, maxMultipartMemory+(1<<20)))
		require.Equal(t, http.StatusInternalServerError, rec.Code, "a disk failure is not image_too_large; body=%s", rec.Body.String())
		body := imageErrorBody(t, rec)
		require.Equal(t, "internal_error", body["error"])
		message, _ := body["message"].(string)
		requireNoInternals(t, message, "nonexistent-review-tmp", "server-secret-dir", "multipart-", "no such file")
	})

	t.Run("aborted body is malformed input", func(t *testing.T) {
		h := newOrphanHarness(t, defaultImageSettings(), "")
		raw, contentType := multipartBody(t, testPNG(t, 40, 30))
		// net/http reports a client that disconnects before Content-Length
		// bytes arrived as io.ErrUnexpectedEOF from the body.
		body := io.MultiReader(bytes.NewReader(raw.Bytes()[:raw.Len()/2]), iotestErrReader{io.ErrUnexpectedEOF})
		req := httptest.NewRequest(http.MethodPost, "/api/requests", body)
		req.Header.Set("Content-Type", contentType)
		req.Header.Set("Authorization", "Bearer sk-error-leak-truncated-1234")
		rec := httptest.NewRecorder()
		h.handler.ServeHTTP(rec, req)
		require.Equal(t, http.StatusUnprocessableEntity, rec.Code, "a cut-off body is not image_too_large; body=%s", rec.Body.String())
		require.Equal(t, "decode_failed", imageErrorBody(t, rec)["error"])
	})

	t.Run("oversized body stays image_too_large", func(t *testing.T) {
		settings := defaultImageSettings()
		settings.PerImageMaxBytes = 1 << 10
		settings.MaxPerRequest = 1
		h := newOrphanHarness(t, settings, "")
		rec := h.post(t, context.Background(), "sk-error-leak-oversize-1234", bytes.Repeat([]byte{'x'}, 3<<20))
		require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code, "body=%s", rec.Body.String())
		require.Equal(t, "image_too_large", imageErrorBody(t, rec)["error"])
	})

	t.Run("client input error keeps its detail", func(t *testing.T) {
		h := newOrphanHarness(t, defaultImageSettings(), "")
		rec := h.post(t, context.Background(), "sk-error-leak-decode-1234", []byte("\x89PNG\r\n\x1a\nnot really a png"))
		require.Equal(t, http.StatusUnprocessableEntity, rec.Code, "body=%s", rec.Body.String())
		body := imageErrorBody(t, rec)
		require.Equal(t, "decode_failed", body["error"])
		message, _ := body["message"].(string)
		require.Contains(t, message, "attachment index 0", "a 4xx keeps the descriptive message")
		require.NotEqual(t, publicImageErrorMessage("decode_failed"), message)
	})
}

// iotestErrReader is a body that fails every read with err.
type iotestErrReader struct{ err error }

// Read returns the configured error.
func (r iotestErrReader) Read([]byte) (int, error) { return 0, r.err }
