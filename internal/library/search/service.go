// Package search provides the web search GraphQL resolvers.
package search

import (
	"context"
	"time"

	gmw "github.com/Laisky/gin-middlewares/v6"
	"github.com/Laisky/zap"
	"github.com/pkg/errors"

	"github.com/Laisky/laisky-blog-graphql/internal/library/models"
	"github.com/Laisky/laisky-blog-graphql/library"
	"github.com/Laisky/laisky-blog-graphql/library/billing/oneapi"
	rlibs "github.com/Laisky/laisky-blog-graphql/library/db/redis"
	"github.com/Laisky/laisky-blog-graphql/library/search"
	"github.com/Laisky/laisky-blog-graphql/library/search/google"
)

// MutationResolver is the resolver for mutation.
type MutationResolver struct {
	engine *google.SearchEngine
	rdb    *rlibs.DB
}

// NewMutationResolver is the constructor for MutationResolver.
func NewMutationResolver(
	engine *google.SearchEngine,
	rdb *rlibs.DB,
) *MutationResolver {
	return &MutationResolver{
		engine: engine,
		rdb:    rdb,
	}
}

// WebFetch is the resolver for webFetch field.
func (r *MutationResolver) WebFetch(ctx context.Context, url string) (*models.WebFetchResult, error) {
	logger := gmw.GetLogger(ctx).
		Named("web_fetch").
		With(zap.String("url", url))
	gctx, ok := gmw.GetGinCtxFromStdCtx(ctx)
	if !ok {
		return nil, errors.New("cannot get gin context from standard context")
	}

	apikey := library.StripBearerPrefix(gctx.GetHeader("Authorization"))
	if apikey == "" {
		return nil, errors.New("cannot get apikey")
	}

	err := oneapi.CheckUserExternalBilling(ctx, apikey, oneapi.PriceWebFetch, "web fetch")
	if err != nil {
		return nil, errors.Wrap(err, "check user external billing")
	}

	result, err := search.FetchDynamicURLContent(ctx, r.rdb, url)
	if err != nil {
		return nil, errors.Wrap(err, "fetch dynamic url content")
	}

	logger.Info("successfully fetch url content")
	return &models.WebFetchResult{
		URL:       url,
		CreatedAt: *library.NewDatetimeFromTime(time.Now()),
		Content:   string(result),
	}, nil
}

// WebSearch is the resolver for webSearch field.
func (r *MutationResolver) WebSearch(ctx context.Context, query string) (*search.SearchResult, error) {
	startAt := time.Now()
	logger := gmw.GetLogger(ctx).
		Named("web_search").
		With(zap.String("query", query))
	gctx, ok := gmw.GetGinCtxFromStdCtx(ctx)
	if !ok {
		return nil, errors.New("cannot get gin context from standard context")
	}

	apikey := library.StripBearerPrefix(gctx.GetHeader("Authorization"))
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

	for _, item := range engineResult.Items {
		result.Results = append(result.Results, search.SearchResultItem{
			URL:     item.Link,
			Name:    item.Title,
			Snippet: item.Snippet,
		})
	}

	logger.Info("successfully search", zap.Duration("cost", time.Since(startAt)))
	return result, nil
}
