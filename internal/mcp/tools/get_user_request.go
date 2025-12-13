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
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/rag"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/userrequests"
)

// UserRequestService exposes the subset of user request operations required by the tool.
type UserRequestService interface {
	ConsumeAllPending(context.Context, *askuser.AuthorizationContext, string) ([]userrequests.Request, error)
	ConsumeFirstPending(context.Context, *askuser.AuthorizationContext, string) (*userrequests.Request, error)
	GetReturnMode(context.Context, *askuser.AuthorizationContext) (string, error)
}

// HoldWaiter waits for a command to be submitted during an active hold.
type HoldWaiter interface {
	IsHoldActive(apiKeyHash string, taskID string) bool
	WaitForCommand(ctx context.Context, apiKeyHash string, taskID string) (*userrequests.Request, bool)
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
		mcp.WithString(
			"task_id",
			mcp.Description("Optional task identifier used to isolate commands; defaults to 'default'."),
		),
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

	taskID := rag.SanitizeTaskID(userrequests.DefaultTaskID)
	if rawTaskID, ok := findTaskIDArg(req.Params.Arguments); ok {
		if parsed := rag.SanitizeTaskID(rawTaskID); parsed != "" {
			taskID = parsed
		}
	}

	// Always honor the user's stored preference; agent-provided overrides are ignored.
	returnMode := "all"
	userPref, prefErr := t.service.GetReturnMode(ctx, authCtx)
	if prefErr != nil {
		t.log().Debug("failed to get user return_mode preference, using default",
			zap.Error(prefErr),
			zap.String("user", authCtx.UserIdentity),
		)
	} else {
		returnMode = userPref
		t.log().Debug("using user's stored return_mode preference",
			zap.String("return_mode", returnMode),
			zap.String("user", authCtx.UserIdentity),
		)
	}

	// If hold is active, first check for existing pending commands.
	// If there are pending commands, return them immediately without waiting.
	// Only wait on hold when there are no pending commands.
	if t.holdWaiter != nil && t.holdWaiter.IsHoldActive(authCtx.APIKeyHash, taskID) {
		// Check for existing pending commands first based on return_mode
		var existingRequests []userrequests.Request
		var existingErr error
		if returnMode == "first" {
			firstReq, err := t.service.ConsumeFirstPending(ctx, authCtx, taskID)
			if err == nil && firstReq != nil {
				existingRequests = []userrequests.Request{*firstReq}
			}
			existingErr = err
		} else {
			existingRequests, existingErr = t.service.ConsumeAllPending(ctx, authCtx, taskID)
		}

		if existingErr == nil && len(existingRequests) > 0 {
			t.log().Info("hold active but pending commands exist, returning immediately",
				zap.String("user", authCtx.UserIdentity),
				zap.Int("count", len(existingRequests)),
				zap.String("return_mode", returnMode),
				zap.String("task_id", taskID),
			)
			return t.buildCommandsResponse(existingRequests)
		}

		// No pending commands, now wait for a command to be submitted
		t.log().Debug("hold active, no pending commands, waiting for new command",
			zap.String("user", authCtx.UserIdentity),
			zap.String("task_id", taskID),
		)
		waitedRequest, timedOut := t.holdWaiter.WaitForCommand(ctx, authCtx.APIKeyHash, taskID)
		if waitedRequest != nil {
			t.log().Info("command received during hold",
				zap.String("user", authCtx.UserIdentity),
				zap.String("request_id", waitedRequest.ID.String()),
				zap.String("task_id", taskID),
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

	// Consume requests based on return_mode
	t.log().Debug("consuming pending requests",
		zap.String("return_mode", returnMode),
		zap.String("user", authCtx.UserIdentity),
		zap.String("task_id", taskID),
	)
	var requests []userrequests.Request
	if returnMode == "first" {
		firstReq, err := t.service.ConsumeFirstPending(ctx, authCtx, taskID)
		if err != nil {
			switch {
			case errors.Is(err, userrequests.ErrInvalidAuthorization):
				t.log().Warn("invalid user request authorization", zap.Error(err))
				return mcp.NewToolResultError("invalid authorization context"), nil
			case errors.Is(err, userrequests.ErrNoPendingRequests):
				t.log().Debug("no pending requests to consume",
					zap.String("return_mode", returnMode),
					zap.String("user", authCtx.UserIdentity),
				)
				return t.emptyResponse(authCtx), nil
			default:
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return mcp.NewToolResultError(http.StatusText(http.StatusRequestTimeout)), nil
				}
				t.log().Error("consume first user request", zap.Error(err))
				return mcp.NewToolResultError("failed to fetch user requests"), nil
			}
		}
		requests = []userrequests.Request{*firstReq}
		t.log().Debug("consumed first pending request",
			zap.String("request_id", firstReq.ID.String()),
			zap.String("user", authCtx.UserIdentity),
			zap.String("task_id", taskID),
		)
	} else {
		var err error
		requests, err = t.service.ConsumeAllPending(ctx, authCtx, taskID)
		if err != nil {
			switch {
			case errors.Is(err, userrequests.ErrInvalidAuthorization):
				t.log().Warn("invalid user request authorization", zap.Error(err))
				return mcp.NewToolResultError("invalid authorization context"), nil
			case errors.Is(err, userrequests.ErrNoPendingRequests):
				t.log().Debug("no pending requests to consume",
					zap.String("return_mode", returnMode),
					zap.String("user", authCtx.UserIdentity),
				)
				return t.emptyResponse(authCtx), nil
			default:
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return mcp.NewToolResultError(http.StatusText(http.StatusRequestTimeout)), nil
				}
				t.log().Error("consume user requests", zap.Error(err))
				return mcp.NewToolResultError("failed to fetch user requests"), nil
			}
		}
		t.log().Debug("consumed all pending requests",
			zap.Int("count", len(requests)),
			zap.String("user", authCtx.UserIdentity),
			zap.String("task_id", taskID),
		)
	}

	if len(requests) == 0 {
		return t.emptyResponse(authCtx), nil
	}

	return t.buildCommandsResponse(requests)
}

func (t *GetUserRequestTool) emptyResponse(auth *askuser.AuthorizationContext) *mcp.CallToolResult {
	message := "user has no new directives, please continue"
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

// buildCommandsResponse constructs the MCP response payload from a list of requests.
func (t *GetUserRequestTool) buildCommandsResponse(requests []userrequests.Request) (*mcp.CallToolResult, error) {
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

func (t *GetUserRequestTool) log() logSDK.Logger {
	if t != nil && t.logger != nil {
		return t.logger
	}
	return logSDK.Shared.Named("get_user_request_tool")
}

func findTaskIDArg(arguments any) (string, bool) {
	args, ok := arguments.(map[string]any)
	if !ok {
		return "", false
	}
	candidates := []string{"task_id", "taskId"}
	for _, key := range candidates {
		if value, ok := args[key]; ok {
			if parsed, ok := stringArg(value); ok {
				return parsed, true
			}
		}
	}
	return "", false
}

func stringArg(value any) (string, bool) {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v), true
	}
	return "", false
}
