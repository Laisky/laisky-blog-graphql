package search

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Laisky/errors/v2"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/library/search/diagnostics"
)

type clientTimeoutEngine struct{ endpoint string }

func (e *clientTimeoutEngine) Name() string { return "synthetic" }
func (e *clientTimeoutEngine) Type() string { return "google" }
func (e *clientTimeoutEngine) Search(ctx context.Context, _ string) ([]SearchResultItem, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.endpoint, nil)
	if err != nil {
		return nil, errors.Wrap(err, "synthetic request")
	}
	response, err := (&http.Client{Timeout: 10 * time.Millisecond}).Do(req)
	if err == nil {
		_ = response.Body.Close()
		return nil, nil
	}
	return nil, diagnostics.Transport(ctx, err)
}

func TestManagerClientTimeoutKeepsHealthyFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	t.Cleanup(server.Close)
	primary := &clientTimeoutEngine{endpoint: server.URL}
	fallback := &testEngine{name: "healthy", items: []SearchResultItem{{Name: "synthetic"}}}
	manager, err := NewManager([][]Engine{{primary}, {fallback}})
	require.NoError(t, err)
	ctx := context.Background()
	output, err := manager.Search(ctx, "synthetic")
	require.NoError(t, ctx.Err(), "client-local timeout must leave parent active")
	require.NoError(t, err, "healthy fallback must run after provider-local timeout")
	require.Equal(t, 1, fallback.calls)
	require.Equal(t, "healthy", output.EngineName)
}
