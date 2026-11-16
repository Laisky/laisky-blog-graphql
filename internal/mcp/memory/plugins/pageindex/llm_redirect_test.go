package pageindex

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestOpenAILLMRefusesRedirects: the Responses call carries the platform's
// API key. A redirect answer must fail the call (never be followed, never be
// decoded as a success) and the key must never reach the redirect target,
// with the default client and with an injected one, which stays unmodified.
func TestOpenAILLMRefusesRedirects(t *testing.T) {
	for _, status := range []int{http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		var targetHits atomic.Int64
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			targetHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(okResponseJSON))
		}))
		redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// A JSON body on the redirect itself must not pass as a success.
			w.Header().Set("Location", target.URL+r.URL.Path)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(okResponseJSON))
		}))

		injected := redirector.Client()
		for _, client := range []*http.Client{nil, injected} {
			llm, err := NewOpenAILLM(LLMConfig{
				APIKey: "sk-platform", BaseURL: redirector.URL, Model: "gpt-5.4-mini", HTTPClient: client,
				Retry: RetrySettings{MaxAttempts: 1, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond},
			})
			require.NoError(t, err)
			resp, err := llm.Respond(context.Background(), Request{Input: []InputItem{{Role: "user", Content: "Hi"}}})
			require.Error(t, err, "status %d", status)
			require.Nil(t, resp)
		}
		require.Nil(t, injected.CheckRedirect, "the injected client is not modified")
		require.Zero(t, targetHits.Load(), "status %d: the platform key must never reach the redirect target", status)
		redirector.Close()
		target.Close()
	}
}
