package search

import "time"

// SearchResult is the result of a search query
type SearchResult struct {
	Query     string             `json:"query"`
	CreatedAt time.Time          `json:"created_at"`
	Results   []SearchResultItem `json:"results"`
}

type SearchResultItem struct {
	URL     string `json:"url"`
	Name    string `json:"name"`
	Snippet string `json:"snippet"`
}
