package search

import "time"

// SearchResult represents the aggregated outcome of a web search query.
// Used by GraphQL API which requires query and created_at fields.
type SearchResult struct {
	Query     string             `json:"query"`
	CreatedAt time.Time          `json:"created_at"`
	Results   []SearchResultItem `json:"results"`
}

// SimplifiedSearchResult is a minimal response for MCP tools.
// It only contains the essential results without auxiliary metadata.
type SimplifiedSearchResult struct {
	Results []SearchResultItem `json:"results"`
}

// SearchResultItem captures a single entry returned by a search provider.
type SearchResultItem struct {
	URL     string `json:"url"`
	Name    string `json:"name"`
	Snippet string `json:"snippet"`
}
