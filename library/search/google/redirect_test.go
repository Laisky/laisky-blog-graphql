package google

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/library/search/diagnostics"
)

// redirectingTransport answers every request for the provider's fixed
// endpoint with a redirect to target (keeping path and query) and forwards
// requests for target to it, so a client that follows the redirect really
// delivers the request to the second server.
type redirectingTransport struct {
	status int
	target *url.URL
}

// RoundTrip redirects provider requests and forwards target requests.
func (rt redirectingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host == rt.target.Host {
		return http.DefaultTransport.RoundTrip(req)
	}
	location := *req.URL
	location.Scheme, location.Host = rt.target.Scheme, rt.target.Host
	return &http.Response{
		StatusCode: rt.status,
		Header:     http.Header{"Location": []string{location.String()}},
		Body:       http.NoBody,
		Request:    req,
	}, nil
}

// TestSearchEngineRefusesRedirects: the provider request carries the
// platform's credential (the key query parameter, which a Location that echoes the query carries along), so a redirect answer must fail like
// any other non-200 status and the request must never reach the redirect
// target, also through an injected client, which stays unmodified.
func TestSearchEngineRefusesRedirects(t *testing.T) {
	for _, status := range []int{http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		var targetHits, leaked atomic.Int64
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			targetHits.Add(1)
			if strings.Contains(r.URL.Query().Get("key"), "sk-platform") {
				leaked.Add(1)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		}))
		targetURL, err := url.Parse(target.URL)
		require.NoError(t, err)

		engine := NewSearchEngine("sk-platform", "cx-id")
		injected := &http.Client{Transport: redirectingTransport{status: status, target: targetURL}}
		engine.client = injected
		answer, err := engine.Search(context.Background(), "query")
		require.Error(t, err, "status %d", status)
		require.Nil(t, answer)
		require.Equal(t, "provider_http", diagnostics.Summary(err).Category, "a redirect is a non-success provider answer")
		require.Equal(t, status, diagnostics.Summary(err).Status)
		require.Nil(t, injected.CheckRedirect, "the configured client is not modified")
		require.Zero(t, targetHits.Load(), "status %d: the request must never reach the redirect target", status)
		require.Zero(t, leaked.Load())
		target.Close()
	}
}
