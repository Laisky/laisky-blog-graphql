package files

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestCohereRerankClientRefusesRedirects: the rerank call carries the
// caller's API key, so a redirect answer must fail like any other non-2xx
// status and the key must never reach the redirect target.
func TestCohereRerankClientRefusesRedirects(t *testing.T) {
	for _, status := range []int{http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		var targetHits atomic.Int64
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			targetHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"results":[{"index":0,"relevance_score":1}]}`))
		}))
		redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL+"/v1/rerank", status)
		}))

		client := NewCohereRerankClient(redirector.URL+"/v1/rerank", "rerank-model", time.Second)
		scores, err := client.Rerank(context.Background(), "sk-caller", "query", []string{"doc"})
		require.Error(t, err, "status %d", status)
		require.Nil(t, scores)
		require.Contains(t, err.Error(), "rerank endpoint status")
		require.Zero(t, targetHits.Load(), "status %d: the caller's key must never reach the redirect target", status)
		redirector.Close()
		target.Close()
	}
}
