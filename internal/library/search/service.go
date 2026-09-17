// Package search provides the web search GraphQL resolvers.
package search

import (
	"context"
	"strings"
	"time"

	"github.com/Laisky/errors/v2"
	gmw "github.com/Laisky/gin-middlewares/v7"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"

	"github.com/Laisky/laisky-blog-graphql/internal/library/models"
	"github.com/Laisky/laisky-blog-graphql/internal/library/toolpolicy"
	mcpauth "github.com/Laisky/laisky-blog-graphql/internal/mcp/auth"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/calllog"
	"github.com/Laisky/laisky-blog-graphql/library"
	"github.com/Laisky/laisky-blog-graphql/library/billing/oneapi"
	rlibs "github.com/Laisky/laisky-blog-graphql/library/db/redis"
	searchlib "github.com/Laisky/laisky-blog-graphql/library/search"
)

// MutationResolver implements the existing GraphQL search and fetch fields.
type MutationResolver struct {
	provider       searchlib.Provider
	rdb            *rlibs.DB
	calllogger     *calllog.Service
	billingChecker func(context.Context, string, oneapi.Price, string) error
	fetcher        func(context.Context, *rlibs.DB, string, string, bool) ([]byte, error)
	validateURL    func(context.Context, string) error
}

// NewMutationResolver constructs a GraphQL adapter over the shared functions.
// MCP registration switches intentionally do not affect these GraphQL fields.
// Missing backend dependencies make only the corresponding operation unavailable.
func NewMutationResolver(provider searchlib.Provider, rdb *rlibs.DB, calllogger *calllog.Service) *MutationResolver {
	return &MutationResolver{provider: provider, rdb: rdb, calllogger: calllogger,
		billingChecker: oneapi.CheckUserExternalBilling,
		fetcher:        searchlib.FetchDynamicURLContent, validateURL: toolpolicy.ValidateFetchURL}
}

func requestAuth(ctx context.Context) (*mcpauth.Context, error) {
	var header string
	if ginCtx, ok := gmw.GetGinCtxFromStdCtx(ctx); ok && ginCtx != nil {
		header = ginCtx.GetHeader("Authorization")
	}
	auth, err := mcpauth.FromContextOrHeader(ctx, header)
	if err != nil {
		return nil, errors.Wrap(err, "authorize shared tool")
	}
	return auth, nil
}
func requestLogger(ctx context.Context, name string) logSDK.Logger {
	logger := gmw.GetLogger(ctx)
	if logger == nil {
		logger = logSDK.Shared
	}
	return logger.Named(name)
}

// WebFetch applies the same admission and canonical identity rules as MCP.
// GraphQL's existing contract returns markdown; the MCP-only format option is
// not silently added to or simulated through an unrelated GraphQL field.
func (r *MutationResolver) WebFetch(ctx context.Context, url string) (*models.WebFetchResult, error) {
	if r.rdb == nil {
		return nil, errors.New("web_fetch is not available")
	}
	auth, err := requestAuth(ctx)
	if err != nil {
		return nil, err
	}
	url = strings.TrimSpace(url)
	if err := r.validateURL(ctx, url); err != nil {
		return nil, errors.Wrap(err, "invalid fetch input")
	}
	startAt := time.Now().UTC()
	logger := requestLogger(ctx, "web_fetch").With(zap.String("url", toolpolicy.URLForLog(url)))
	if err := r.billingChecker(ctx, auth.APIKey, oneapi.PriceWebFetch, "web_fetch"); err != nil {
		return nil, errors.Wrap(err, "check user external billing")
	}
	content, err := r.fetcher(ctx, r.rdb, url, auth.APIKey, true)
	r.record(ctx, logger, auth.APIKey, "web_fetch", oneapi.PriceWebFetch,
		map[string]any{"url": toolpolicy.URLForLog(url), "output_markdown": true}, startAt, err)
	if err != nil {
		return nil, errors.Wrap(err, "fetch dynamic url content")
	}
	return &models.WebFetchResult{URL: url, CreatedAt: *library.NewDatetimeFromTime(time.Now().UTC()), Content: string(content)}, nil
}

// WebSearch validates before charging and returns the established GraphQL shape.
func (r *MutationResolver) WebSearch(ctx context.Context, query string) (*searchlib.SearchResult, error) {
	if r.provider == nil {
		return nil, errors.New("web_search is not available")
	}
	auth, err := requestAuth(ctx)
	if err != nil {
		return nil, err
	}
	query, err = toolpolicy.Query(query)
	if err != nil {
		return nil, errors.Wrap(err, "invalid search input")
	}
	startAt := time.Now().UTC()
	logger := requestLogger(ctx, "web_search").With(zap.Int("query_len", len(query)))
	if err := r.billingChecker(ctx, auth.APIKey, oneapi.PriceWebSearch, "web_search"); err != nil {
		return nil, errors.Wrap(err, "check user external billing")
	}
	output, err := r.provider.Search(ctx, query)
	if err == nil && output == nil {
		err = errors.New("search provider returned no result")
	}
	params := map[string]any{"query": query}
	if output != nil {
		params["engine_name"], params["engine_type"] = output.EngineName, output.EngineType
	}
	r.record(ctx, logger, auth.APIKey, "web_search", oneapi.PriceWebSearch, params, startAt, err)
	if err != nil {
		return nil, errors.Wrap(err, "search provider failed")
	}
	return &searchlib.SearchResult{Query: query, CreatedAt: time.Now().UTC(), EngineName: output.EngineName,
		EngineType: output.EngineType, Results: append([]searchlib.SearchResultItem{}, output.Items...)}, nil
}

// record keeps one resolver-level audit record for each attempted provider call.
// The GraphQL resolver does not also invoke MCP's billing/audit wrapper.
func (r *MutationResolver) record(ctx context.Context, logger logSDK.Logger, apiKey, tool string,
	price oneapi.Price, parameters map[string]any, started time.Time, callErr error) {
	if r.calllogger == nil {
		return
	}
	status, message := calllog.StatusSuccess, ""
	if callErr != nil {
		status, message = calllog.StatusError, callErr.Error()
	}
	if err := r.calllogger.Record(ctx, calllog.RecordInput{ToolName: tool, APIKey: apiKey, Status: status,
		Cost: price.Int(), Duration: time.Since(started), Parameters: parameters, ErrorMessage: message, OccurredAt: started}); err != nil {
		logger.Warn("record call log", zap.Error(err))
	}
}
