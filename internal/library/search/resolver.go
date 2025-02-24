package search

import (
	"context"

	"github.com/Laisky/laisky-blog-graphql/library"
	"github.com/Laisky/laisky-blog-graphql/library/search"
)

// WebSearchResultResolver
type WebSearchResultResolver struct{}

// CreatedAt resolves the created_at field for WebSearchResultResolver
func (r *WebSearchResultResolver) CreatedAt(ctx context.Context, obj *search.SearchResult) (*library.Datetime, error) {
	return library.NewDatetimeFromTime(obj.CreatedAt), nil
}
