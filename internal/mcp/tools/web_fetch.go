package tools

import (
	"context"
	"strings"
	"time"

	"github.com/Laisky/errors/v2"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/google/uuid"
	mcp "github.com/mark3labs/mcp-go/mcp"

	"github.com/Laisky/laisky-blog-graphql/internal/library/toolpolicy"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/ctxkeys"
	"github.com/Laisky/laisky-blog-graphql/library/billing/oneapi"
	"github.com/Laisky/laisky-blog-graphql/library/crawleregress"
	rlibs "github.com/Laisky/laisky-blog-graphql/library/db/redis"
	appLog "github.com/Laisky/laisky-blog-graphql/library/log"
	"github.com/Laisky/laisky-blog-graphql/library/search"
)

// DynamicFetcher retrieves rendered HTML content for a given URL.
type DynamicFetcher func(ctx context.Context, store *rlibs.DB, url string, apiKey string, outputMarkdown bool) ([]byte, error)

// WebFetchTool implements the web_fetch MCP tool.
type WebFetchTool struct {
	store          *rlibs.DB
	logger         logSDK.Logger
	apiKeyProvider APIKeyProvider
	billingChecker BillingChecker
	fetcher        DynamicFetcher
}

// NewWebFetchTool constructs a WebFetchTool with the provided dependencies.
func NewWebFetchTool(store *rlibs.DB, logger logSDK.Logger, apiKeyProvider APIKeyProvider, billingChecker BillingChecker, fetcher DynamicFetcher) (*WebFetchTool, error) {
	if store == nil {
		return nil, errors.New("redis client is required")
	}
	if logger == nil {
		return nil, errors.New("logger is required")
	}
	if apiKeyProvider == nil {
		return nil, errors.New("api key provider is required")
	}
	if billingChecker == nil {
		return nil, errors.New("billing checker is required")
	}
	if fetcher == nil {
		return nil, errors.New("dynamic fetcher is required")
	}

	return &WebFetchTool{
		store:          store,
		logger:         logger,
		apiKeyProvider: apiKeyProvider,
		billingChecker: billingChecker,
		fetcher:        fetcher,
	}, nil
}

// Definition returns the MCP metadata describing the tool.
func (t *WebFetchTool) Definition() mcp.Tool {
	return mcp.NewTool(
		"web_fetch",
		mcp.WithDescription("Fetch and render dynamic web content by URL. Downloads a web page and returns its content as HTML or Markdown. Use this to retrieve, read, or scrape a specific webpage."),
		mcp.WithString(
			"url",
			mcp.Required(),
			mcp.Description("The URL to retrieve."),
		),
		mcp.WithBoolean(
			"output_markdown",
			mcp.Description("Whether to return Markdown instead of raw HTML."),
			mcp.DefaultBool(true),
		),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(true),
	)
}

// Handle executes the web_fetch tool logic using the configured dependencies.
func (t *WebFetchTool) Handle(ctx context.Context, req mcp.CallToolRequest) (result *mcp.CallToolResult, handleErr error) {
	logger := reviewedWebFetchLogger(t.logger)
	requestID, _ := ctx.Value(ctxkeys.RequestID).(string)
	parsedID, idErr := uuid.Parse(requestID)
	if idErr != nil || len(requestID) != 36 || parsedID == uuid.Nil || parsedID.String() != requestID {
		requestID = uuid.NewString()
	}
	logger = logger.With(zap.String("request_id", requestID))
	ctx = search.WithFetchLogger(ctx, logger)
	urlValue, err := req.RequireString("url")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	originalURL := urlValue
	var capture *webFetchCapture
	captureFailure := search.FetchFailureDiagnostic{ErrorCode: "tool_input_rejected", Stage: "admission"}
	defer func() {
		if capture != nil && result != nil && result.IsError && !capture.attempted {
			logger.Warn("web_fetch failure journal", capture.writeFailure(captureFailure)...)
		}
	}()

	urlValue = strings.TrimSpace(urlValue)
	if urlValue == "" {
		return mcp.NewToolResultError("url cannot be empty"), nil
	}

	apiKey := t.apiKeyProvider(ctx)
	if apiKey == "" {
		logger.Warn("web_fetch missing api key")
		return mcp.NewToolResultError("missing authorization bearer token"), nil
	}

	captureFailure = search.FetchFailureDiagnostic{ErrorCode: "tool_input_rejected", Stage: "admission"}
	outputMarkdown, err := toolpolicy.OptionalBool(req.GetArguments(), "output_markdown", true)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	capture = newWebFetchCapture(req, originalURL, requestID)
	capture.record.Request.OutputMarkdown = outputMarkdown
	if err := toolpolicy.ValidateFetchURL(ctx, urlValue); err != nil {
		return mcp.NewToolResultError("invalid url"), nil //nolint:nilerr // Validation failures are MCP tool results, not transport failures.
	}

	start := time.Now().UTC()
	logger.Debug("web_fetch started",
		zap.Bool("output_markdown", outputMarkdown),
	)
	logger.Debug("web_fetch billing check started",
		zap.Bool("output_markdown", outputMarkdown),
	)

	captureFailure = search.FetchFailureDiagnostic{ErrorCode: "billing_failed", Stage: "billing"}
	if err := t.billingChecker(ctx, apiKey, oneapi.PriceWebFetch, "web_fetch"); err != nil {
		logger.Warn("web_fetch billing denied", zap.Error(toolBillingLogError(err)))
		return mcp.NewToolResultError("billing check failed"), nil
	}

	logger.Debug("web_fetch billing check passed",
		zap.Bool("output_markdown", outputMarkdown),
	)

	content, err := t.fetcher(ctx, t.store, urlValue, apiKey, outputMarkdown)
	if err != nil {
		diagnostic := search.FetchDiagnostic(err)
		targetScheme, targetHostname := crawleregress.TargetComponents(urlValue)
		fields := appLog.WebFetchDiagnosticFields(appLog.WebFetchFailureSummary{
			ErrorCode: diagnostic.ErrorCode, Stage: diagnostic.Stage, TaskID: diagnostic.TaskID,
			TargetScheme: targetScheme, TargetHostname: targetHostname, WorkerContext: diagnostic.WorkerContext,
			WorkerStage: diagnostic.WorkerStage, WorkerCategory: diagnostic.WorkerCategory, WorkerSignal: diagnostic.WorkerSignal,
			CallerContextState: search.FetchCallerContextState(ctx), ErrorLifecycle: search.FetchErrorLifecycle(err),
			DurationMS: time.Since(start).Milliseconds(),
		})
		fields = append(fields, zap.Error(search.FetchLogError(err)))
		logger.Error("web_fetch failed", capture.writeFailure(diagnostic, fields...)...)
		return mcp.NewToolResultError("fetch failed: " + diagnostic.ErrorCode), nil
	}

	payload := map[string]any{
		contentKey: string(content),
	}

	captureFailure = search.FetchFailureDiagnostic{ErrorCode: "result_encoding_failed", Stage: "response"}
	toolResult, err := mcp.NewToolResultJSON(payload)
	if err != nil {
		logger.Error("encode web_fetch result", zap.Error(err))
		return mcp.NewToolResultError("failed to encode web_fetch response"), nil
	}

	// This event establishes response construction, not transport delivery or client receipt.
	logger.Info("web_fetch response prepared", zap.String("event", "response_prepared"),
		zap.String("stage", "response"), zap.String("caller_context_state", search.FetchCallerContextState(ctx)),
		zap.Int64("duration_ms", min(max(time.Since(start).Milliseconds(), 0), 24*60*60*1000)))

	return toolResult, nil
}

func reviewedWebFetchLogger(fallback logSDK.Logger) logSDK.Logger {
	if named, ok := fallback.(interface{ Name() string }); ok && strings.HasSuffix(named.Name(), ".web_fetch") {
		return fallback
	}
	return fallback.Named("web_fetch")
}

// toolBillingLogError preserves billing outcome/status without logging the response body in Reason.
func toolBillingLogError(err error) error {
	var billing *oneapi.BillingError
	if errors.As(err, &billing) && billing != nil {
		outcome := oneapi.ClassifyBillingOutcome(err)
		if billing.Status >= 100 && billing.Status <= 599 {
			return errors.Errorf("billing request failed: outcome=%s HTTP %d", outcome, billing.Status)
		}
		return errors.Errorf("billing request failed: outcome=%s", outcome)
	}
	return search.FetchLogError(err)
}
