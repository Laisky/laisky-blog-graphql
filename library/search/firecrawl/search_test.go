package firecrawl

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/library/search"
)

func TestSearchEngineSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
		require.Equal(t, "application/json", r.Header.Get("Content-Type"))

		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)

		var reqPayload searchRequest
		require.NoError(t, json.Unmarshal(body, &reqPayload))
		require.Equal(t, "test-query", reqPayload.Query)
		require.Equal(t, []string{firecrawlWebSourceID}, reqPayload.Sources)
		require.Equal(t, 5, reqPayload.Limit)

		payload := map[string]any{
			"success": true,
			"data": map[string]any{
				"web": []map[string]string{
					{"url": "https://example.com", "title": "Example", "description": "Snippet"},
					{"url": "", "title": "Skipped", "description": "no url"},
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(payload))
	}))
	defer server.Close()

	engine := NewSearchEngine("test-key",
		WithEndpoint(server.URL),
		WithHTTPClient(server.Client()),
		WithLimit(5),
	)

	items, err := engine.Search(context.Background(), "test-query")
	require.NoError(t, err)
	require.Equal(t, []search.SearchResultItem{{URL: "https://example.com", Name: "Example", Snippet: "Snippet"}}, items)
}

func TestSearchEngineReportsAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"error":"quota exceeded"}`))
	}))
	defer server.Close()

	engine := NewSearchEngine("key", WithEndpoint(server.URL), WithHTTPClient(server.Client()))

	items, err := engine.Search(context.Background(), "query")
	require.Error(t, err)
	require.Nil(t, items)
	require.Contains(t, err.Error(), "quota exceeded")
}

func TestSearchEngineHandlesHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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

func TestSearchEngineValidatesQuery(t *testing.T) {
	engine := NewSearchEngine("key")

	items, err := engine.Search(context.Background(), "   ")
	require.Error(t, err)
	require.Nil(t, items)
	require.Contains(t, err.Error(), "query")
}

func TestSearchEngineNameAndType(t *testing.T) {
	engine := NewSearchEngine("key", WithName("custom_firecrawl"))
	require.Equal(t, "custom_firecrawl", engine.Name())
	require.Equal(t, firecrawlEngineType, engine.Type())

	defaultEngine := NewSearchEngine("key")
	require.Equal(t, firecrawlEngineName, defaultEngine.Name())
}
