package benchmark

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// redirectPair starts a target that counts every request and a redirector
// that answers status with a Location on the target.
func redirectPair(t *testing.T, status int) (redirector *httptest.Server, targetHits *atomic.Int64) {
	t.Helper()
	targetHits = new(atomic.Int64)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{},"output_text":"leaked"}`))
	}))
	t.Cleanup(target.Close)
	redirector = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, status)
	}))
	t.Cleanup(redirector.Close)
	return redirector, targetHits
}

// TestBenchmarkClientsRefuseRedirects: the benchmark MCP client and reader
// carry an Authorization credential, so a redirect answer fails like any
// other unexpected status and never reaches the redirect target, with the
// default client and with an injected one, which stays unmodified.
func TestBenchmarkClientsRefuseRedirects(t *testing.T) {
	for _, status := range []int{http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		redirector, targetHits := redirectPair(t, status)
		injected := redirector.Client()
		for _, client := range []*http.Client{nil, injected} {
			mcpClient, err := NewMCPClient(redirector.URL+"/mcp", "Bearer sk-bench", "", client)
			require.NoError(t, err)
			require.Error(t, mcpClient.Initialize(context.Background()), "status %d", status)
			mcpClient.sessionID = "session-1"
			require.Error(t, mcpClient.Close(context.Background()), "status %d: closing the session", status)

			answerer, err := NewResponsesAnswerer(redirector.URL+"/v1/responses", "sk-reader", "model", client)
			require.NoError(t, err)
			result, err := answerer.Answer(context.Background(), Query{Text: "q"}, nil)
			require.Error(t, err, "status %d", status)
			require.Nil(t, result)
		}
		require.Nil(t, injected.CheckRedirect, "the injected client is not modified")
		require.Zero(t, targetHits.Load(), "status %d: the credential must never reach the redirect target", status)
	}
}
