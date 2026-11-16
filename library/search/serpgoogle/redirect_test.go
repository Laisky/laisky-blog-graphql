package serpgoogle

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/library/search/diagnostics"
)

// TestSearchEngineRefusesRedirects: the provider request carries the
// platform's credential (the api_key query parameter), so a redirect answer must fail like
// any other non-200 status and the credential must never reach the redirect
// target, whether the engine uses its default client or an injected one. Go
// keeps Authorization on a same-host redirect to another port, forwards
// custom headers to every target, and a Location that echoes the query
// carries a query credential along.
func TestSearchEngineRefusesRedirects(t *testing.T) {
	for _, status := range []int{http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		var targetHits atomic.Int64
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			targetHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		}))
		redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, "sk-platform", r.URL.Query().Get("api_key"))
			http.Redirect(w, r, target.URL+r.URL.RequestURI(), status)
		}))

		injected := redirector.Client()
		for _, opts := range [][]Option{{WithEndpoint(redirector.URL)}, {WithEndpoint(redirector.URL), WithHTTPClient(injected)}} {
			engine := NewSearchEngine("sk-platform", opts...)
			items, err := engine.Search(context.Background(), "query")
			require.Error(t, err, "status %d", status)
			require.Nil(t, items)
			require.Equal(t, "provider_http", diagnostics.Summary(err).Category, "a redirect is a non-success provider answer")
			require.Equal(t, status, diagnostics.Summary(err).Status)
		}
		require.Nil(t, injected.CheckRedirect, "the injected client is not modified")
		require.Zero(t, targetHits.Load(), "status %d: the credential must never reach the redirect target", status)
		redirector.Close()
		target.Close()
	}
}
