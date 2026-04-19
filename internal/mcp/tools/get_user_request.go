package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
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

// ImageIssuer is the abstraction the tool uses to serve image attachments back
// to agents. It exposes the two operations from userrequests.ImageManager —
// presigning the object URL and fetching the raw bytes for inlining — so tests
// can substitute a deterministic fake without importing MinIO.
type ImageIssuer interface {
	PresignURL(ctx context.Context, image userrequests.RequestImage) (string, error)
	FetchInline(ctx context.Context, image userrequests.RequestImage) ([]byte, error)
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
	imageIssuer    ImageIssuer
	budget         ImageBudgetConfig
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
		budget:         DefaultImageBudget(),
	}, nil
}

// WithImageIssuer attaches an ImageIssuer so the tool can emit image + link
// content for requests that carry attachments. Passing nil disables image
// emission entirely, preserving the pure-text response shape.
func (t *GetUserRequestTool) WithImageIssuer(issuer ImageIssuer) *GetUserRequestTool {
	if t != nil {
		t.imageIssuer = issuer
	}
	return t
}

// Definition returns the metadata describing the tool to MCP clients.
// The response may be mixed content (text + inline image + resource_link) when
// the user attached images; Anthropic-specific `_meta` hints declare the text
// portion's size budget so Claude Code can plan its context usage.
func (t *GetUserRequestTool) Definition() mcp.Tool {
	tool := mcp.NewTool(
		"get_user_request",
		mcp.WithDescription("During the execution of a task, get the latest user instructions to adjust agent's work objectives or processes. The response may include text, inline images, and resource links when the user attached images."),
		mcp.WithIdempotentHintAnnotation(false),
		mcp.WithString(
			"task_id",
			mcp.Description("Optional task identifier used to isolate commands; defaults to 'default'."),
		),
	)
	if tool.Meta == nil {
		tool.Meta = &mcp.Meta{}
	}
	if tool.Meta.AdditionalFields == nil {
		tool.Meta.AdditionalFields = map[string]any{}
	}
	tool.Meta.AdditionalFields["anthropic/maxResultSizeChars"] = 20000
	return tool
}

// Handle executes the core tool logic.
func (t *GetUserRequestTool) Handle(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) { //nolint:gocognit // tool handler with multiple parameter extraction steps
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
			for i, r := range existingRequests {
				t.log().Debug("returning pending command during hold",
					zap.Int("index", i),
					zap.String("request_id", r.ID.String()),
					zap.Int("sort_order", r.SortOrder),
				)
			}
			return t.buildCommandsResponse(ctx, existingRequests)
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
			// Route the waited command through the same builder so image
			// payloads surface identically whether the command landed via
			// hold or via the regular consume path.
			return t.buildCommandsResponse(ctx, []userrequests.Request{*waitedRequest})
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
		for i, r := range requests {
			t.log().Debug("returning pending command",
				zap.Int("index", i),
				zap.String("request_id", r.ID.String()),
				zap.Int("sort_order", r.SortOrder),
			)
		}
	}

	if len(requests) == 0 {
		return t.emptyResponse(authCtx), nil
	}

	return t.buildCommandsResponse(ctx, requests)
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

// buildCommandsResponse constructs the MCP response payload from a list of
// requests. When no attachments are present the response is byte-identical to
// NewToolResultJSON for backwards compatibility. Otherwise the result carries
// a mixed content sequence: text summary, optional ImageContent blocks for
// images that fit the inline budget, and ResourceLink blocks for every image.
func (t *GetUserRequestTool) buildCommandsResponse(ctx context.Context, requests []userrequests.Request) (*mcp.CallToolResult, error) {
	hasImages := false
	for _, r := range requests {
		if len(r.Images) > 0 {
			hasImages = true
			break
		}
	}

	if !hasImages || t.imageIssuer == nil {
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

	return t.buildMixedCommandsResponse(ctx, requests)
}

// buildMixedCommandsResponse composes the dual-channel payload described in
// the proposal §3.2: one TextContent per command (JSON serialization with
// image metadata and inline placeholders), then ImageContent / ResourceLink
// blocks for each attachment, followed by structuredContent.
func (t *GetUserRequestTool) buildMixedCommandsResponse(ctx context.Context, requests []userrequests.Request) (*mcp.CallToolResult, error) {
	content := make([]mcp.Content, 0, len(requests)*3)
	structuredCommands := make([]map[string]any, 0, len(requests))

	for i, req := range requests {
		issuedImages := make([]map[string]any, 0, len(req.Images))
		resolved := make([]resolvedImage, 0, len(req.Images))
		for _, img := range req.Images {
			url, err := t.imageIssuer.PresignURL(ctx, img)
			if err != nil {
				t.log().Warn("presign image for tool response",
					zap.String("image_id", img.ID.String()),
					zap.Error(err),
				)
				continue
			}
			meta := map[string]any{
				"id":         img.ID.String(),
				"mime":       img.MIMEType,
				"url":        url,
				"width":      img.Width,
				"height":     img.Height,
				"sha256":     img.SHA256,
				"expires_at": img.ExpiresAt,
			}
			issuedImages = append(issuedImages, meta)
			resolved = append(resolved, resolvedImage{meta: img, url: url})
		}

		cmdSummary := map[string]any{
			"command": i,
			"content": req.Content,
			"images":  issuedImages,
		}
		jsonBytes, err := json.Marshal(cmdSummary)
		if err != nil {
			t.log().Error("marshal command summary", zap.Error(err))
			return mcp.NewToolResultError("failed to encode response"), nil
		}
		content = append(content, mcp.TextContent{Type: "text", Text: string(jsonBytes)})

		// Plan inline budgets based on base64 size estimates; fetch inline
		// bytes only for images that stay under the ceiling.
		sizeEstimates := make([]int, 0, len(resolved))
		for _, r := range resolved {
			sizeEstimates = append(sizeEstimates, base64.StdEncoding.EncodedLen(int(r.meta.SizeBytes)))
		}
		decisions := PlanInlineBudget(sizeEstimates, t.budget)
		for idx, decision := range decisions {
			img := resolved[idx]
			if decision.Inline {
				body, err := t.imageIssuer.FetchInline(ctx, img.meta)
				if err != nil {
					t.log().Warn("fetch inline image bytes", zap.String("image_id", img.meta.ID.String()), zap.Error(err))
				} else {
					content = append(content, mcp.ImageContent{
						Type:     "image",
						Data:     base64.StdEncoding.EncodeToString(body),
						MIMEType: img.meta.MIMEType,
					})
				}
			}
			content = append(content, mcp.ResourceLink{
				Type:     "resource_link",
				URI:      img.url,
				Name:     img.meta.SHA256 + ".png",
				MIMEType: img.meta.MIMEType,
			})
		}

		structuredCommands = append(structuredCommands, cmdSummary)
	}

	return &mcp.CallToolResult{
		Content: content,
		StructuredContent: map[string]any{
			"commands":         structuredCommands,
			"protocol_version": "v2",
		},
	}, nil
}

type resolvedImage struct {
	meta userrequests.RequestImage
	url  string
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
