package userrequests

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/imageproc"
)

// TestHandleCreateDecodeMemoryBudget covers the HTTP contract of the decode
// memory budget: an image whose estimated working set exceeds the whole budget
// is rejected as decode_failed (422), and a request that cannot be admitted
// before its context ends gets decode_busy (503). Neither uploads anything.
func TestHandleCreateDecodeMemoryBudget(t *testing.T) {
	t.Run("estimate over budget", func(t *testing.T) {
		settings := defaultImageSettings()
		settings.DecodeMemoryBudgetBytes = 2 << 20
		h := newOrphanHarness(t, settings, "")
		rec := h.post(t, context.Background(), "sk-budget-over-1234567", testPNG(t, 900, 700))
		require.Equal(t, http.StatusUnprocessableEntity, rec.Code, "body=%s", rec.Body.String())
		require.Equal(t, "decode_failed", errorCode(t, rec))
		require.Contains(t, rec.Body.String(), "decode memory budget")
		require.Empty(t, h.store.PutKeys())
	})

	t.Run("not admitted before the request ends", func(t *testing.T) {
		fetcher := &stubFetcher{result: imageproc.FetchResult{Body: testPNG(t, 40, 30), MIMEHint: "image/png"}}
		handler, _, store := buildImageHandler(t, "img-budget-busy", defaultImageSettings(), fetcher)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		payload := []byte(`{"content":"x","image_urls":["https://example.com/a.png"]}`)
		req := httptest.NewRequest(http.MethodPost, "/api/requests", bytes.NewReader(payload)).WithContext(ctx)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer sk-budget-busy-1234567")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		require.Equal(t, http.StatusServiceUnavailable, rec.Code, "body=%s", rec.Body.String())
		require.Equal(t, "decode_busy", errorCode(t, rec))
		require.Zero(t, store.ObjectCount())
	})
}
