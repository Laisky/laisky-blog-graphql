package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestResponsesHelperRefusesRedirects: the Responses call carries the
// caller's API key, so a redirect answer must fail like any other non-2xx
// status and the key must never reach the redirect target, also with an
// injected client, which must stay unmodified.
func TestResponsesHelperRefusesRedirects(t *testing.T) {
	t.Parallel()

	for _, status := range []int{http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		var targetHits atomic.Int64
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			targetHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"output_text":"leaked"}`))
		}))
		redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL+r.URL.Path, status)
		}))

		injected := redirector.Client()
		for _, client := range []*http.Client{nil, injected} {
			helper := NewResponsesHelper(redirector.URL, 2*time.Second, client)
			text, err := helper.CreateText(context.Background(), "sk-caller", ResponseRequest{Model: "m", Input: "hello"})
			require.Error(t, err, "status %d", status)
			require.Empty(t, text)
			require.Contains(t, err.Error(), "responses endpoint status")
		}
		require.Nil(t, injected.CheckRedirect, "the injected client is not modified")
		require.Zero(t, targetHits.Load(), "status %d: the caller's key must never reach the redirect target", status)
		redirector.Close()
		target.Close()
	}
}
