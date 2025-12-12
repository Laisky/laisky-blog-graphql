package tools

import (
	"context"
	"fmt"
	"strings"

	errors "github.com/Laisky/errors/v2"
	gutils "github.com/Laisky/go-utils/v6"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	mcp "github.com/mark3labs/mcp-go/mcp"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/rag"
	"github.com/Laisky/laisky-blog-graphql/library/billing/oneapi"
)

// KeyInfoService defines the subset of rag.Service methods required by the tool.
type KeyInfoService interface {
	ExtractKeyInfo(context.Context, rag.ExtractInput) ([]string, error)
}

// ExtractKeyInfoTool exposes the extract_key_info MCP capability.
type ExtractKeyInfoTool struct {
	service        KeyInfoService
	logger         logSDK.Logger
	headerProvider AuthorizationHeaderProvider
	billingChecker BillingChecker
	settings       rag.Settings
}

// NewExtractKeyInfoTool wires the dependencies for the tool handler.
func NewExtractKeyInfoTool(service KeyInfoService, logger logSDK.Logger, headerProvider AuthorizationHeaderProvider, billingChecker BillingChecker, settings rag.Settings) (*ExtractKeyInfoTool, error) {
	if service == nil {
		return nil, errors.New("rag service is required")
	}
	if logger == nil {
		return nil, errors.New("logger is required")
	}
	if headerProvider == nil {
		return nil, errors.New("authorization header provider is required")
	}
	if billingChecker == nil {
		return nil, errors.New("billing checker is required")
	}
	if settings.TopKDefault == 0 {
		settings = rag.LoadSettingsFromConfig()
	}

	return &ExtractKeyInfoTool{
		service:        service,
		logger:         logger,
		headerProvider: headerProvider,
		billingChecker: billingChecker,
		settings:       settings,
	}, nil
}

// Definition returns the MCP metadata for the tool contract.
func (t *ExtractKeyInfoTool) Definition() mcp.Tool {
	return mcp.NewTool(
		"extract_key_info",
		mcp.WithDescription("Extract the most relevant context chunks for a query from provided materials."),
		mcp.WithString(
			"query",
			mcp.Required(),
			mcp.Description("User question or query."),
		),
		mcp.WithString(
			"materials",
			mcp.Required(),
			mcp.Description("Source text to scan for relevant information."),
		),
		mcp.WithNumber(
			"top_k",
			mcp.Description("Maximum number of contexts to return."),
		),
	)
}

// Handle executes the extract_key_info workflow for the MCP server.
func (t *ExtractKeyInfoTool) Handle(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	query, err := req.RequireString("query")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	materials, err := req.RequireString("materials")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	query = strings.TrimSpace(query)
	materials = strings.TrimSpace(materials)
	if query == "" {
		return mcp.NewToolResultError("query cannot be empty"), nil
	}
	if materials == "" {
		return mcp.NewToolResultError("materials cannot be empty"), nil
	}
	if len(materials) > t.settings.MaxMaterialsSize {
		return mcp.NewToolResultError(fmt.Sprintf("materials exceed maximum size (%d bytes)", t.settings.MaxMaterialsSize)), nil
	}

	taskID := rag.SanitizeTaskID(gutils.UUID7())
	if taskID == "" {
		t.logger.Error("extract_key_info generated empty task id")
		return mcp.NewToolResultError("failed to initialize request"), nil
	}

	topK := t.settings.TopKDefault
	if rawTopK, ok := findTopK(req.Params.Arguments); ok {
		topK = rawTopK
	}
	if topK <= 0 || topK > t.settings.TopKLimit {
		return mcp.NewToolResultError(fmt.Sprintf("top_k must be between 1 and %d", t.settings.TopKLimit)), nil
	}

	header := t.headerProvider(ctx)
	identity, err := rag.ParseIdentity(header)
	if err != nil {
		t.logger.Warn("extract_key_info authorization failed", zap.Error(err))
		return mcp.NewToolResultError(err.Error()), nil
	}

	if err := t.billingChecker(ctx, identity.APIKey, oneapi.PriceExtractKeyInfo, "extract_key_info"); err != nil {
		t.logger.Warn("extract_key_info billing denied", zap.Error(err))
		return mcp.NewToolResultError(fmt.Sprintf("billing check failed: %v", err)), nil
	}

	input := rag.ExtractInput{
		UserID:    identity.UserID,
		TaskID:    taskID,
		APIKey:    identity.APIKey,
		Query:     query,
		Materials: materials,
		TopK:      topK,
	}

	contexts, err := t.service.ExtractKeyInfo(ctx, input)
	if err != nil {
		t.logger.Error("extract_key_info failed", zap.Error(err), zap.String("user_id", identity.UserID), zap.String("task_id", taskID))
		return mcp.NewToolResultError("failed to extract key information"), nil
	}

	payload := map[string]any{
		"contexts": contexts,
	}

	result, err := mcp.NewToolResultJSON(payload)
	if err != nil {
		t.logger.Error("extract_key_info encode response", zap.Error(err))
		return mcp.NewToolResultError("failed to encode extract_key_info response"), nil
	}

	return result, nil
}

func findTopK(arguments any) (int, bool) {
	args, ok := arguments.(map[string]any)
	if !ok {
		return 0, false
	}
	candidates := []string{"top_k", "topK"}
	for _, key := range candidates {
		if value, ok := args[key]; ok {
			if parsed, ok := toInt(value); ok {
				return parsed, true
			}
		}
	}
	return 0, false
}

func toInt(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case int32:
		return int(v), true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	case float32:
		return int(v), true
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return 0, false
		}
		var parsed int
		if _, err := fmt.Sscanf(trimmed, "%d", &parsed); err == nil {
			return parsed, true
		}
	}
	return 0, false
}
