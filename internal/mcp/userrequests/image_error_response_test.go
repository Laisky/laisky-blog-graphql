package userrequests

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

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

	t.Run("body read deadline", func(t *testing.T) {
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
