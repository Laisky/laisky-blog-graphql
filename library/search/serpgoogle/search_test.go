package serpgoogle

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/library/search"
)

func TestSearchEngineSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "GET", r.Method)
		require.Equal(t, "test-query", r.URL.Query().Get("q"))
		require.Equal(t, "test-key", r.URL.Query().Get("api_key"))
		require.Equal(t, "google", r.URL.Query().Get("engine"))

		payload := map[string]any{
			"organic_results": []map[string]string{
				{"link": "https://example.com", "title": "Example", "snippet": "Snippet"},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(payload))
	}))
	defer server.Close()

	client := server.Client()
	engine := NewSearchEngine("test-key", WithEndpoint(server.URL), WithHTTPClient(client))

	items, err := engine.Search(context.Background(), "test-query")
	require.NoError(t, err)
	require.Equal(t, []search.SearchResultItem{{URL: "https://example.com", Name: "Example", Snippet: "Snippet"}}, items)
}

func TestSearchEngineReportsAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"error":"quota"}`))
	}))
	defer server.Close()

	engine := NewSearchEngine("key", WithEndpoint(server.URL), WithHTTPClient(server.Client()))

	items, err := engine.Search(context.Background(), "query")
	require.Error(t, err)
	require.Nil(t, items)
	require.Contains(t, err.Error(), "quota")
}

func TestSearchEngineHandlesHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"server"}`))
	}))
	defer server.Close()

	engine := NewSearchEngine("key", WithEndpoint(server.URL), WithHTTPClient(server.Client()))

	items, err := engine.Search(context.Background(), "query")
	require.Error(t, err)
	require.Nil(t, items)
	require.Contains(t, err.Error(), "returned status")
}

func TestSearchEngineValidatesAPIKey(t *testing.T) {
	engine := NewSearchEngine("")

	items, err := engine.Search(context.Background(), "query")
	require.Error(t, err)
	require.Nil(t, items)
	require.Contains(t, err.Error(), "api key")
}
