package httpclient

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestWithoutRedirectsReturnsTheRedirectAndKeepsBase covers the policy and
// the copy semantics: the caller sees the 3xx, the target never sees the
// credential, and the base client keeps its own redirect policy and settings.
func TestWithoutRedirectsReturnsTheRedirectAndKeepsBase(t *testing.T) {
	var reached atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached.Add(1) }))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusPermanentRedirect)
	}))
	defer origin.Close()

	transport := &http.Transport{}
	base := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	for _, client := range []*http.Client{WithoutRedirects(base), WithoutRedirects(nil)} {
		request, err := http.NewRequest(http.MethodGet, origin.URL, nil)
		require.NoError(t, err)
		request.Header.Set("Authorization", "Bearer sk-policy-probe")
		response, err := client.Do(request)
		require.NoError(t, err)
		require.NoError(t, response.Body.Close())
		require.Equal(t, http.StatusPermanentRedirect, response.StatusCode)
	}
	require.Zero(t, reached.Load())

	copied := WithoutRedirects(base)
	require.Same(t, transport, copied.Transport, "the copy shares the connection pool")
	require.Equal(t, base.Timeout, copied.Timeout)
	require.Nil(t, base.CheckRedirect, "base is never modified")
	require.NotSame(t, base, copied)
}
