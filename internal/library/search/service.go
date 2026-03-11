// Package search provides the web search GraphQL resolvers.
package search

import (
	"context"
	"time"

	"github.com/Laisky/errors/v2"
	gmw "github.com/Laisky/gin-middlewares/v7"
	"github.com/Laisky/zap"

	"github.com/Laisky/laisky-blog-graphql/internal/library/models"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/calllog"
	"github.com/Laisky/laisky-blog-graphql/library"
	"github.com/Laisky/laisky-blog-graphql/library/billing/oneapi"
	rlibs "github.com/Laisky/laisky-blog-graphql/library/db/redis"
	searchlib "github.com/Laisky/laisky-blog-graphql/library/search"
)

// MutationResolver is the resolver for mutation.
type MutationResolver struct {
	provider   searchlib.Provider
	rdb        *rlibs.DB
	calllogger *calllog.Service
}

// NewMutationResolver is the constructor for MutationResolver.
func NewMutationResolver(provider searchlib.Provider, rdb *rlibs.DB, calllogger *calllog.Service) *MutationResolver {
	return &MutationResolver{
		provider:   provider,
		rdb:        rdb,
		calllogger: calllogger,
	}
}

// WebFetch is the resolver for webFetch field.
func (r *MutationResolver) WebFetch(ctx context.Context, url string) (*models.WebFetchResult, error) {
	startAt := time.Now()
	const outputMarkdown = true
	logger := gmw.GetLogger(ctx).
		Named("web_fetch").
		With(
			zap.String("url", url),
			zap.Bool("output_markdown", outputMarkdown),
		)
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

	logger.Debug("fetch dynamic url content started")

	result, err := searchlib.FetchDynamicURLContent(ctx, r.rdb, url, apikey, outputMarkdown)
	status := calllog.StatusSuccess
	var errMsg string
	if err != nil {
		status = calllog.StatusError
		errMsg = err.Error()
	}

	if r.calllogger != nil {
		if recordErr := r.calllogger.Record(ctx, calllog.RecordInput{
			ToolName:     "web_fetch",
			APIKey:       apikey,
			Status:       status,
			Cost:         oneapi.PriceWebFetch.Int(),
			Duration:     time.Since(startAt),
			Parameters:   map[string]any{"url": url, "output_markdown": outputMarkdown},
			ErrorMessage: errMsg,
			OccurredAt:   startAt,
		}); recordErr != nil {
			logger.Warn("record call log", zap.Error(recordErr))
		}
	}

	if err != nil {
		return nil, errors.Wrap(err, "fetch dynamic url content")
	}

	logger.Debug("fetch dynamic url content completed", zap.Int("content_len", len(result)))
	logger.Info("successfully fetch url content")
	return &models.WebFetchResult{
		URL:       url,
		CreatedAt: *library.NewDatetimeFromTime(time.Now()),
		Content:   string(result),
	}, nil
}

// WebSearch is the resolver for webSearch field.
func (r *MutationResolver) WebSearch(ctx context.Context, query string) (*searchlib.SearchResult, error) {
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

	if r.provider == nil {
		return nil, errors.New("web search provider is not configured")
	}

	output, err := r.provider.Search(ctx, query)
	status := calllog.StatusSuccess
	var errMsg string
	if err != nil {
		status = calllog.StatusError
		errMsg = err.Error()
	}

	if r.calllogger != nil {
		params := map[string]any{"query": query}
		if output != nil {
			params["engine_name"] = output.EngineName
			params["engine_type"] = output.EngineType
		}
		if recordErr := r.calllogger.Record(ctx, calllog.RecordInput{
			ToolName:     "web_search",
			APIKey:       apikey,
			Status:       status,
			Cost:         oneapi.PriceWebSearch.Int(),
			Duration:     time.Since(startAt),
			Parameters:   params,
			ErrorMessage: errMsg,
			OccurredAt:   startAt,
		}); recordErr != nil {
			logger.Warn("record call log", zap.Error(recordErr))
		}
	}

	if err != nil {
		return nil, errors.Wrapf(err, "failed to search for query `%s`", query)
	}

	result := &searchlib.SearchResult{
		Query:      query,
		CreatedAt:  time.Now(),
		EngineName: output.EngineName,
		EngineType: output.EngineType,
	}

	result.Results = append(result.Results, output.Items...)

	logger.Info("successfully search", zap.Duration("cost", time.Since(startAt)))
	return result, nil
}
