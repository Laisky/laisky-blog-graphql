// Package bing is a GraphQL resolver for Bing search engine.
package bing

import (
	"context"
	"time"

	"github.com/Laisky/laisky-blog-graphql/library/search"
	"github.com/Laisky/laisky-blog-graphql/library/search/bing"
	"github.com/pkg/errors"
)

// MutationResolver is the resolver for mutation.
type MutationResolver struct {
	engine *bing.SearchEngine
}

// NewMutationResolver is the constructor for MutationResolver.
func NewMutationResolver(engine *bing.SearchEngine) *MutationResolver {
	return &MutationResolver{
		engine: engine,
	}
}

// WebSearch is the resolver for webSearch field.
func (r *MutationResolver) WebSearch(ctx context.Context, query string) (*search.SearchResult, error) {
	engineResult, err := r.engine.Search(ctx, query)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to search for query `%s`", query)
	}

	result := &search.SearchResult{
		Query:     query,
		CreatedAt: time.Now(),
	}

	for _, item := range engineResult.WebPages.Value {
		result.Results = append(result.Results, search.SearchResultItem{
			URL:     item.URL,
			Name:    item.Name,
			Snippet: item.Snippet,
		})
	}

	return result, nil
}
