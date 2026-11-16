package rag

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestOpenAIEmbedderRefusesRedirects: the embeddings call carries the
// caller's API key, so a redirect answer must fail like any other non-2xx
// status and the key must never reach the redirect target. Go's own policy
// would keep Authorization on a subdomain, another port or an https→http
// downgrade of the same host.
func TestOpenAIEmbedderRefusesRedirects(t *testing.T) {
	for _, status := range []int{http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		var targetHits atomic.Int64
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			targetHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"embedding":[0.1]}]}`))
		}))
		redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL+r.URL.Path, status)
		}))

		for _, client := range []*http.Client{nil, redirector.Client()} {
			embedder := NewOpenAIEmbedder(redirector.URL, "test-model", client)
			vectors, err := embedder.EmbedTexts(context.Background(), "sk-caller", []string{"hello"})
			require.Error(t, err, "status %d", status)
			require.Nil(t, vectors)
			require.Contains(t, err.Error(), "embeddings endpoint status")
		}
		require.Zero(t, targetHits.Load(), "status %d: the caller's key must never reach the redirect target", status)
		redirector.Close()
		target.Close()
	}
}
