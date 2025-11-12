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

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/askuser"
)

// AskUserService defines the subset of ask_user.Service methods required by the tool.
type AskUserService interface {
	CreateRequest(context.Context, *askuser.AuthorizationContext, string) (*askuser.Request, error)
	WaitForAnswer(context.Context, uuid.UUID) (*askuser.Request, error)
	CancelRequest(context.Context, uuid.UUID, string) error
}

// AuthorizationParser converts an authorization header into an ask_user authorization context.
type AuthorizationParser func(string) (*askuser.AuthorizationContext, error)

// AskUserTool implements the ask_user MCP tool.
type AskUserTool struct {
	service        AskUserService
	logger         logSDK.Logger
	headerProvider AuthorizationHeaderProvider
	parser         AuthorizationParser
	timeout        time.Duration
}

const defaultAskUserTimeout = 5 * time.Minute

// NewAskUserTool constructs an AskUserTool with the provided dependencies.
func NewAskUserTool(service AskUserService, logger logSDK.Logger, headerProvider AuthorizationHeaderProvider, parser AuthorizationParser, timeout time.Duration) (*AskUserTool, error) {
	if service == nil {
		return nil, errors.New("ask_user service is required")
	}
	if logger == nil {
		return nil, errors.New("logger is required")
	}
	if headerProvider == nil {
		return nil, errors.New("authorization header provider is required")
	}
	if parser == nil {
		return nil, errors.New("authorization parser is required")
	}
	if timeout <= 0 {
		timeout = defaultAskUserTimeout
	}

	return &AskUserTool{
		service:        service,
		logger:         logger,
		headerProvider: headerProvider,
		parser:         parser,
		timeout:        timeout,
	}, nil
}

// Definition returns the MCP metadata describing the tool.
func (t *AskUserTool) Definition() mcp.Tool {
	return mcp.NewTool(
		"ask_user",
		mcp.WithDescription("Forward a question to the authenticated user and wait for a response."),
		mcp.WithString(
			"question",
			mcp.Required(),
			mcp.Description("The question that should be surfaced to the user."),
		),
		mcp.WithIdempotentHintAnnotation(false),
	)
}

// Handle executes the ask_user tool logic using the configured dependencies.
func (t *AskUserTool) Handle(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	question, err := req.RequireString("question")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	question = strings.TrimSpace(question)
	if question == "" {
		return mcp.NewToolResultError("question cannot be empty"), nil
	}

	authHeader := t.headerProvider(ctx)
	authCtx, err := t.parser(authHeader)
	if err != nil {
		t.logger.Warn("ask_user authorization failed", zap.Error(err))
		return mcp.NewToolResultError("invalid authorization header"), nil
	}

	callCtx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()

	stored, err := t.service.CreateRequest(callCtx, authCtx, question)
	if err != nil {
		t.logger.Error("ask_user create request", zap.Error(err))
		return mcp.NewToolResultError("failed to create ask_user request"), nil
	}

	answered, err := t.service.WaitForAnswer(callCtx, stored.ID)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			_ = t.service.CancelRequest(context.Background(), stored.ID, askuser.StatusExpired)
			return mcp.NewToolResultError("timeout waiting for user response"), nil
		}

		t.logger.Error("ask_user wait for answer", zap.Error(err))
		return mcp.NewToolResultError("failed while waiting for user response"), nil
	}

	if answered.Answer == nil {
		return mcp.NewToolResultError("user responded without an answer"), nil
	}

	resultPayload := map[string]any{
		"request_id": answered.ID.String(),
		"question":   answered.Question,
		"answer":     *answered.Answer,
		"asked_at":   answered.CreatedAt,
	}

	if answered.AnsweredAt != nil {
		resultPayload["answered_at"] = *answered.AnsweredAt
	}

	toolResult, err := mcp.NewToolResultJSON(resultPayload)
	if err != nil {
		t.logger.Error("encode ask_user response", zap.Error(err))
		return mcp.NewToolResultError("failed to encode ask_user response"), nil
	}

	return toolResult, nil
}
