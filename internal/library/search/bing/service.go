// Package bing is a GraphQL resolver for Bing search engine.
package bing

import (
	"context"
	"strings"
	"time"

	gmw "github.com/Laisky/gin-middlewares/v6"
	"github.com/Laisky/laisky-blog-graphql/library/billing/oneapi"
	"github.com/Laisky/laisky-blog-graphql/library/search"
	"github.com/Laisky/laisky-blog-graphql/library/search/bing"
	"github.com/Laisky/zap"
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
	startAt := time.Now()
	logger := gmw.GetLogger(ctx).
		Named("web_search").
		With(zap.String("query", query))
	gctx := gmw.GetGinCtxFromStdCtx(ctx)
	apikey := strings.TrimSpace(strings.TrimPrefix(gctx.GetHeader("Authorization"), "Bearer "))
	if apikey == "" {
		return nil, errors.New("cannot get apikey")
	}

	err := oneapi.CheckUserExternalBilling(ctx, apikey, oneapi.PriceWebSearch, "web search")
	if err != nil {
		return nil, errors.Wrap(err, "check user external billing")
	}

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

	logger.Info("successfully search", zap.Duration("cost", time.Since(startAt)))
	return result, nil
}
