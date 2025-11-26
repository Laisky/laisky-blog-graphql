package tools

import (
	"context"
	"net/http"
	"strings"

	errors "github.com/Laisky/errors/v2"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/askuser"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/userrequests"
)

// UserRequestService exposes the subset of user request operations required by the tool.
type UserRequestService interface {
	ConsumeLatestPending(context.Context, *askuser.AuthorizationContext) (*userrequests.Request, error)
}

// GetUserRequestTool streams the newest pending human directive back to the AI agent.
type GetUserRequestTool struct {
	service        UserRequestService
	logger         logSDK.Logger
	headerProvider AuthorizationHeaderProvider
	parser         AuthorizationParser
}

// NewGetUserRequestTool constructs the tool with the required dependencies.
func NewGetUserRequestTool(service UserRequestService, logger logSDK.Logger, headerProvider AuthorizationHeaderProvider, parser AuthorizationParser) (*GetUserRequestTool, error) {
	if service == nil {
		return nil, errors.New("user request service is required")
	}
	if headerProvider == nil {
		return nil, errors.New("authorization header provider is required")
	}
	if parser == nil {
		return nil, errors.New("authorization parser is required")
	}
	if logger == nil {
		logger = logSDK.Shared.Named("get_user_request_tool")
	}

	return &GetUserRequestTool{
		service:        service,
		logger:         logger,
		headerProvider: headerProvider,
		parser:         parser,
	}, nil
}

// Definition returns the metadata describing the tool to MCP clients.
func (t *GetUserRequestTool) Definition() mcp.Tool {
	return mcp.NewTool(
		"get_user_request",
		mcp.WithDescription("During the execution of a task, get the latest user instructions to adjust agent's work objectives or processes."),
		mcp.WithIdempotentHintAnnotation(false),
	)
}

// Handle executes the core tool logic.
func (t *GetUserRequestTool) Handle(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	authHeader := t.headerProvider(ctx)
	authCtx, err := t.parser(authHeader)
	if err != nil {
		t.log().Warn("get_user_request authorization failed", zap.Error(err))
		return mcp.NewToolResultError("invalid authorization header"), nil
	}

	request, err := t.service.ConsumeLatestPending(ctx, authCtx)
	switch {
	case errors.Is(err, userrequests.ErrInvalidAuthorization):
		t.log().Warn("invalid user request authorization", zap.Error(err))
		return mcp.NewToolResultError("invalid authorization context"), nil
	case errors.Is(err, userrequests.ErrNoPendingRequests):
		return t.emptyResponse(authCtx), nil
	case err != nil:
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return mcp.NewToolResultError(http.StatusText(http.StatusRequestTimeout)), nil
		}
		t.log().Error("consume user request", zap.Error(err))
		return mcp.NewToolResultError("failed to fetch user request"), nil
	case request == nil:
		return t.emptyResponse(authCtx), nil
	}

	payload := map[string]any{
		"content": request.Content,
	}

	result, encodeErr := mcp.NewToolResultJSON(payload)
	if encodeErr != nil {
		t.log().Error("encode get_user_request response", zap.Error(encodeErr))
		return mcp.NewToolResultError("failed to encode response"), nil
	}
	return result, nil
}

func (t *GetUserRequestTool) emptyResponse(auth *askuser.AuthorizationContext) *mcp.CallToolResult {
	message := "user has no new directives"
	if auth != nil {
		message = strings.Join([]string{message, "for", auth.UserIdentity}, " ")
	}
	result, err := mcp.NewToolResultJSON(map[string]any{
		"status":  "empty",
		"message": message,
	})
	if err != nil {
		return mcp.NewToolResultError("failed to encode empty response")
	}
	return result
}

func (t *GetUserRequestTool) log() logSDK.Logger {
	if t != nil && t.logger != nil {
		return t.logger
	}
	return logSDK.Shared.Named("get_user_request_tool")
}
