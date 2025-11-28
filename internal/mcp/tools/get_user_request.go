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
	ConsumeAllPending(context.Context, *askuser.AuthorizationContext) ([]userrequests.Request, error)
}

// HoldWaiter waits for a command to be submitted during an active hold.
type HoldWaiter interface {
	IsHoldActive(apiKeyHash string) bool
	WaitForCommand(ctx context.Context, apiKeyHash string) (*userrequests.Request, bool)
}

// GetUserRequestTool streams pending human directives back to the AI agent.
type GetUserRequestTool struct {
	service        UserRequestService
	holdWaiter     HoldWaiter
	logger         logSDK.Logger
	headerProvider AuthorizationHeaderProvider
	parser         AuthorizationParser
}

// NewGetUserRequestTool constructs the tool with the required dependencies.
// holdWaiter may be nil if hold functionality is not enabled.
func NewGetUserRequestTool(service UserRequestService, holdWaiter HoldWaiter, logger logSDK.Logger, headerProvider AuthorizationHeaderProvider, parser AuthorizationParser) (*GetUserRequestTool, error) {
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
		holdWaiter:     holdWaiter,
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

	// If hold is active, wait for a command to be submitted
	if t.holdWaiter != nil && t.holdWaiter.IsHoldActive(authCtx.APIKeyHash) {
		t.log().Debug("hold active, waiting for command",
			zap.String("user", authCtx.UserIdentity),
		)
		waitedRequest, timedOut := t.holdWaiter.WaitForCommand(ctx, authCtx.APIKeyHash)
		if waitedRequest != nil {
			t.log().Info("command received during hold",
				zap.String("user", authCtx.UserIdentity),
				zap.String("request_id", waitedRequest.ID.String()),
			)
			// Return the single waited command as a list
			payload := map[string]any{
				"commands": []map[string]any{
					{"content": waitedRequest.Content},
				},
			}
			result, encodeErr := mcp.NewToolResultJSON(payload)
			if encodeErr != nil {
				t.log().Error("encode get_user_request response", zap.Error(encodeErr))
				return mcp.NewToolResultError("failed to encode response"), nil
			}
			return result, nil
		}
		if timedOut {
			t.log().Info("hold timeout without user command",
				zap.String("user", authCtx.UserIdentity),
			)
			return t.holdTimeoutResponse(authCtx), nil
		}
		// Hold released without a command, fall through to normal flow
		t.log().Debug("hold released without command, checking for pending requests",
			zap.String("user", authCtx.UserIdentity),
		)
	}

	requests, err := t.service.ConsumeAllPending(ctx, authCtx)
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
		t.log().Error("consume user requests", zap.Error(err))
		return mcp.NewToolResultError("failed to fetch user requests"), nil
	case len(requests) == 0:
		return t.emptyResponse(authCtx), nil
	}

	// Build list of commands in FIFO order
	commands := make([]map[string]any, len(requests))
	for i, r := range requests {
		commands[i] = map[string]any{
			"content": r.Content,
		}
	}

	payload := map[string]any{
		"commands": commands,
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

func (t *GetUserRequestTool) holdTimeoutResponse(auth *askuser.AuthorizationContext) *mcp.CallToolResult {
	message := "User is still typing their next directive. Call get_user_request again later to retrieve it."
	result, err := mcp.NewToolResultJSON(map[string]any{
		"status":  "hold_timeout",
		"message": message,
	})
	if err != nil {
		return mcp.NewToolResultError("failed to encode hold-timeout response")
	}
	return result
}

func (t *GetUserRequestTool) log() logSDK.Logger {
	if t != nil && t.logger != nil {
		return t.logger
	}
	return logSDK.Shared.Named("get_user_request_tool")
}
