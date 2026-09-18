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
	// auditHook observes the parameters written to the audit row. It exists so
	// the recorded copy can be asserted without a call-log database, and is
	// never set in production.
	auditHook func(map[string]any)
}

// NewMutationResolver constructs a GraphQL adapter over the shared functions.
// MCP registration switches intentionally do not affect these GraphQL fields.
// Missing backend dependencies make only the corresponding operation unavailable.
func NewMutationResolver(provider searchlib.Provider, rdb *rlibs.DB, calllogger *calllog.Service) *MutationResolver {
	return &MutationResolver{provider: provider, rdb: rdb, calllogger: calllogger,
		billingChecker: oneapi.CheckUserExternalBilling,
		fetcher:        searchlib.FetchDynamicURLContent, validateURL: toolpolicy.ValidateFetchURL}
}

// requestAuth resolves the caller identity through the shared MCP
// normalization, falling back to the raw gin Authorization header, so tenant
// isolation is identical on GraphQL and MCP.
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

// requestLogger returns the request-scoped logger for one shared tool field.
// gmw.GetLogger always yields a usable logger, falling back to the shared one.
func requestLogger(ctx context.Context, name string) logSDK.Logger {
	return gmw.GetLogger(ctx).Named(name)
}

// defaultFetchOutputMarkdown is the established default for both interfaces.
// It stays true so an existing client that sends no selection is unaffected.
const defaultFetchOutputMarkdown = true

// WebFetch applies the same admission and canonical identity rules as MCP, and
// accepts the same output-format selection. An omitted or null outputMarkdown
// keeps the Markdown default; false returns the raw HTML body. The response
// reports the format that was actually requested from the renderer.
func (r *MutationResolver) WebFetch(ctx context.Context, url string, outputMarkdown *bool) (*models.WebFetchResult, error) {
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
	markdown := defaultFetchOutputMarkdown
	if outputMarkdown != nil {
		markdown = *outputMarkdown
	}
	startAt := time.Now().UTC()
	logger := requestLogger(ctx, "web_fetch").With(zap.String("url", toolpolicy.URLForLog(url)),
		zap.Bool("output_markdown", markdown))
	parameters := map[string]any{"url": toolpolicy.URLForLog(url), "output_markdown": markdown}
	if billingErr := r.billingChecker(ctx, auth.APIKey, oneapi.PriceWebFetch, "web_fetch"); billingErr != nil {
		// A denied or undetermined consume is audited too, so the trail shows
		// the attempt and its classified outcome instead of nothing at all.
		r.record(ctx, logger, auth.APIKey, "web_fetch", oneapi.PriceWebFetch, parameters, startAt, billingErr)
		return nil, errors.Wrap(billingErr, "check user external billing")
	}
	content, err := r.fetcher(ctx, r.rdb, url, auth.APIKey, markdown)
	r.record(ctx, logger, auth.APIKey, "web_fetch", oneapi.PriceWebFetch, parameters, startAt, err)
	if err != nil {
		return nil, errors.Wrap(err, "fetch dynamic url content")
	}
	return &models.WebFetchResult{URL: url, CreatedAt: *library.NewDatetimeFromTime(time.Now().UTC()),
		Content: string(content), OutputMarkdown: markdown}, nil
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
	if billingErr := r.billingChecker(ctx, auth.APIKey, oneapi.PriceWebSearch, "web_search"); billingErr != nil {
		r.record(ctx, logger, auth.APIKey, "web_search", oneapi.PriceWebSearch,
			map[string]any{"query": query}, startAt, billingErr)
		return nil, errors.Wrap(billingErr, "check user external billing")
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
//
// The recorded cost follows the classified billing outcome, not the provider
// result: a consume that was accepted stays charged even when the provider then
// failed, and a denied consume records zero. An undetermined consume is marked
// indeterminate so the row is not read as a receipt.
func (r *MutationResolver) record(ctx context.Context, logger logSDK.Logger, apiKey, tool string,
	price oneapi.Price, parameters map[string]any, started time.Time, callErr error) {
	if r.auditHook != nil {
		r.auditHook(parameters)
	}
	if r.calllogger == nil {
		return
	}
	status, message := calllog.StatusSuccess, ""
	if callErr != nil {
		status, message = calllog.StatusError, callErr.Error()
	}
	if err := r.calllogger.Record(ctx, calllog.RecordInput{ToolName: tool, APIKey: apiKey, Status: status,
		Cost: price.Int(), Duration: time.Since(started), Parameters: parameters, ErrorMessage: message,
		OccurredAt: started, Billing: BillingMetadataFor(price, callErr)}); err != nil {
		logger.Warn("record call log", zap.Error(err))
	}
}

// BillingMetadataFor classifies one invocation's billing outcome for the audit
// row. It is shared so MCP, GraphQL and any later interface describe the same
// situation identically.
//
// A nil error means the consume was accepted. A billing failure carries its own
// classification; any other error happened after an accepted consume, so the
// charge stands even though the operation failed.
func BillingMetadataFor(price oneapi.Price, callErr error) *calllog.BillingMetadata {
	outcome := oneapi.BillingAccepted
	if callErr != nil {
		var billingErr *oneapi.BillingError
		if errors.As(callErr, &billingErr) {
			outcome = oneapi.ClassifyBillingOutcome(callErr)
		}
	}
	return &calllog.BillingMetadata{Outcome: string(outcome), Price: price.Int(),
		Charged: outcome.Charged(), Indeterminate: outcome.Indeterminate()}
}
