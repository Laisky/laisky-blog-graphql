// Package rag provides the extract_key_info GraphQL resolvers, exposing the
// same retrieval-augmented context extraction that the MCP `extract_key_info`
// tool offers.
package rag

import (
	"context"
	"strings"
	"time"

	"github.com/Laisky/errors/v2"
	gmw "github.com/Laisky/gin-middlewares/v7"
	gutils "github.com/Laisky/go-utils/v6"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"

	"github.com/Laisky/laisky-blog-graphql/internal/library/models"
	mcpauth "github.com/Laisky/laisky-blog-graphql/internal/mcp/auth"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/calllog"
	ragsvc "github.com/Laisky/laisky-blog-graphql/internal/mcp/rag"
	"github.com/Laisky/laisky-blog-graphql/library"
	"github.com/Laisky/laisky-blog-graphql/library/billing/oneapi"
)

// KeyInfoService defines the subset of rag.Service methods required by the resolver.
type KeyInfoService interface {
	ExtractKeyInfo(context.Context, ragsvc.ExtractInput) ([]string, error)
}

// BillingChecker validates external billing quotas for extract_key_info requests.
type BillingChecker func(ctx context.Context, apiKey string, price oneapi.Price, scene string) error

// MutationResolver is the resolver for the RAG mutations.
type MutationResolver struct {
	service        KeyInfoService
	settings       ragsvc.Settings
	calllogger     *calllog.Service
	billingChecker BillingChecker
}

// Option customizes a MutationResolver.
type Option func(*MutationResolver)

// WithBillingChecker overrides the default external billing checker.
func WithBillingChecker(checker BillingChecker) Option {
	return func(r *MutationResolver) {
		if checker != nil {
			r.billingChecker = checker
		}
	}
}

// NewMutationResolver is the constructor for MutationResolver. A nil service
// disables the mutation, which then returns a clean error instead of panicking.
func NewMutationResolver(service KeyInfoService, settings ragsvc.Settings,
	calllogger *calllog.Service, opts ...Option) *MutationResolver {
	r := &MutationResolver{
		service:        service,
		settings:       normalizeSettings(settings),
		calllogger:     calllogger,
		billingChecker: oneapi.CheckUserExternalBilling,
	}
	for _, opt := range opts {
		opt(r)
	}

	return r
}

// normalizeSettings falls back to the shared configuration when the caller
// passes a zero-valued Settings, mirroring the MCP tool behavior.
func normalizeSettings(settings ragsvc.Settings) ragsvc.Settings {
	if settings.TopKDefault == 0 {
		return ragsvc.LoadSettingsFromConfig()
	}

	return settings
}

// ExtractKeyInfo is the resolver for the ExtractKeyInfo field. It mirrors the
// MCP `extract_key_info` tool: it splits the materials into chunks, embeds
// them, and returns the top-K passages that best match the query.
func (r *MutationResolver) ExtractKeyInfo(ctx context.Context,
	query string, materials string, topK *int) (*models.ExtractKeyInfoResult, error) {
	startAt := time.Now()
	logger := gmw.GetLogger(ctx).Named("extract_key_info")

	if r.service == nil {
		return nil, errors.New("rag service is not configured")
	}

	query = strings.TrimSpace(query)
	materials = strings.TrimSpace(materials)
	if query == "" {
		return nil, errors.New("query cannot be empty")
	}
	if materials == "" {
		return nil, errors.New("materials cannot be empty")
	}
	if len(materials) > r.settings.MaxMaterialsSize {
		return nil, errors.Errorf("materials exceed maximum size (%d bytes)", r.settings.MaxMaterialsSize)
	}

	limit := r.settings.TopKDefault
	if topK != nil {
		limit = *topK
	}
	if limit <= 0 || limit > r.settings.TopKLimit {
		return nil, errors.Errorf("top_k must be between 1 and %d", r.settings.TopKLimit)
	}

	authCtx, err := r.authorize(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "authorize extract_key_info")
	}

	if err := r.billingChecker(ctx, authCtx.APIKey, oneapi.PriceExtractKeyInfo, "extract_key_info"); err != nil {
		return nil, errors.Wrap(err, "check user external billing")
	}

	taskID := ragsvc.SanitizeTaskID(gutils.UUID7())
	if taskID == "" {
		return nil, errors.New("failed to initialize request")
	}

	logger = logger.With(
		zap.String("user_id", authCtx.UserID),
		zap.String("task_id", taskID),
		zap.Int("top_k", limit),
		zap.Int("materials_size", len(materials)),
	)
	logger.Debug("extract key info started")

	contexts, err := r.service.ExtractKeyInfo(ctx, ragsvc.ExtractInput{
		UserID:    authCtx.UserID,
		TaskID:    taskID,
		APIKey:    authCtx.APIKey,
		Query:     query,
		Materials: materials,
		TopK:      limit,
	})
	r.record(ctx, logger, authCtx.APIKey, query, len(materials), limit, startAt, err)
	if err != nil {
		return nil, errors.Wrap(err, "extract key info")
	}

	if contexts == nil {
		contexts = []string{}
	}

	logger.Info("successfully extract key info",
		zap.Int("contexts", len(contexts)),
		zap.Duration("cost", time.Since(startAt)),
	)

	return &models.ExtractKeyInfoResult{
		Query:     query,
		CreatedAt: *library.NewDatetimeFromTime(time.Now()),
		Contexts:  contexts,
	}, nil
}

// authorize resolves the caller identity from the request context, falling back
// to the raw Authorization header carried by the gin request.
func (r *MutationResolver) authorize(ctx context.Context) (*mcpauth.Context, error) {
	var header string
	if gctx, ok := gmw.GetGinCtxFromStdCtx(ctx); ok {
		header = gctx.GetHeader("Authorization")
	}

	authCtx, err := mcpauth.FromContextOrHeader(ctx, header)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	return authCtx, nil
}

// record persists the call log entry for auditing and billing reconciliation.
func (r *MutationResolver) record(ctx context.Context, logger logSDK.Logger, apiKey, query string,
	materialsSize, topK int, startAt time.Time, callErr error) {
	if r.calllogger == nil {
		return
	}

	status := calllog.StatusSuccess
	var errMsg string
	if callErr != nil {
		status = calllog.StatusError
		errMsg = callErr.Error()
	}

	if recordErr := r.calllogger.Record(ctx, calllog.RecordInput{
		ToolName: "extract_key_info",
		APIKey:   apiKey,
		Status:   status,
		Cost:     oneapi.PriceExtractKeyInfo.Int(),
		Duration: time.Since(startAt),
		Parameters: map[string]any{
			"query":          query,
			"materials_size": materialsSize,
			"top_k":          topK,
		},
		ErrorMessage: errMsg,
		OccurredAt:   startAt,
	}); recordErr != nil {
		logger.Warn("record call log", zap.Error(recordErr))
	}
}
