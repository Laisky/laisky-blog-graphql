package mcp

import (
	"context"
	"fmt"
	"strings"
	"time"

	errors "github.com/Laisky/errors/v2"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	mcp "github.com/mark3labs/mcp-go/mcp"

	mcpauth "github.com/Laisky/laisky-blog-graphql/internal/mcp/auth"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/calllog"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/ctxkeys"
	"github.com/Laisky/laisky-blog-graphql/library/billing/oneapi"
	"github.com/Laisky/laisky-blog-graphql/library/log"
)

// handleWebSearch runs the web_search tool through the shared audit wrapper.
func (s *Server) handleWebSearch(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var exec toolExecutor
	if s.webSearch != nil {
		exec = s.webSearch.Handle
	}

	return s.executeToolHandler(ctx, req, "web_search", oneapi.PriceWebSearch, "web search is not configured", exec)
}

// handleWebFetch executes the web_fetch MCP tool. The context carries request metadata,
// and the request supplies the target URL. It returns a structured response when the
// fetch succeeds or a tool error when processing fails.
func (s *Server) handleWebFetch(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var exec toolExecutor
	if s.webFetch != nil {
		exec = s.webFetch.Handle
	}

	return s.executeToolHandler(ctx, req, "web_fetch", oneapi.PriceWebFetch, "web fetch is not configured", exec)
}

// extractAPIKey returns the bearer token from an Authorization header value.
func extractAPIKey(authHeader string) string {
	parsed, err := mcpauth.ParseAuthorizationContext(authHeader)
	if err != nil {
		return ""
	}

	return parsed.APIKey
}

// apiKeyFromContext returns the request API key stored by the transport.
func apiKeyFromContext(ctx context.Context) string {
	if authCtx, ok := mcpauth.FromContext(ctx); ok {
		return authCtx.APIKey
	}

	authHeader, _ := ctx.Value(keyAuthorization).(string)
	return extractAPIKey(authHeader)
}

type billingAttemptContextKey struct{}

type billingAttemptState struct {
	attempted bool
	// outcome is the classification of the consume call this invocation made.
	// It is what the audit row must follow, because the tool result says
	// whether the operation succeeded, not whether quota was spent.
	outcome oneapi.BillingOutcome
}

type toolExecutor func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)

// withBillingAttemptTracking attaches mutable billing attempt state to one tool invocation.
// Parameters:
//   - ctx: request context passed through MCP tool execution.
//
// Returns:
//   - child context carrying centralized billing attempt state for the current tool invocation.
func withBillingAttemptTracking(ctx context.Context) context.Context {
	return context.WithValue(ctx, billingAttemptContextKey{}, &billingAttemptState{})
}

// markBillingOutcome records both the attempt and its classified result.
// Parameters:
//   - ctx: request context carrying billing attempt state.
//   - outcome: classification of the consume call.
func markBillingOutcome(ctx context.Context, outcome oneapi.BillingOutcome) {
	tracker, ok := ctx.Value(billingAttemptContextKey{}).(*billingAttemptState)
	if !ok || tracker == nil {
		return
	}

	tracker.attempted = true
	tracker.outcome = outcome
}

// billingOutcomeFromContext returns the classified consume outcome for this
// invocation, and whether one was recorded at all.
// Parameters:
//   - ctx: request context carrying billing attempt state.
//
// Returns:
//   - the recorded outcome and true, or the zero value and false when this
//     invocation never reached centralized billing.
func billingOutcomeFromContext(ctx context.Context) (oneapi.BillingOutcome, bool) {
	tracker, ok := ctx.Value(billingAttemptContextKey{}).(*billingAttemptState)
	if !ok || tracker == nil || tracker.outcome == "" {
		return "", false
	}

	return tracker.outcome, true
}

// trackBillingOutcome wraps the shared billing reporter so every paid tool
// classifies its consume result in one place instead of each tool remembering.
// Parameters:
//   - reporter: the underlying centralized billing reporter.
//
// Returns:
//   - a reporter that records the classified outcome before returning.
func trackBillingOutcome(reporter func(context.Context, string, oneapi.Price, string) error,
) func(context.Context, string, oneapi.Price, string) error {
	return func(ctx context.Context, apiKey string, price oneapi.Price, toolName string) error {
		err := reporter(ctx, apiKey, price, toolName)
		markBillingOutcome(ctx, oneapi.ClassifyBillingOutcome(err))
		return err
	}
}

// billingMetadataForInvocation classifies one invocation for its audit row.
//
// An invocation that never reached centralized billing spent nothing, so it is
// recorded as not_attempted with the configured price for reference. Otherwise
// the recorded outcome decides whether the price counts as charged.
func billingMetadataForInvocation(ctx context.Context, baseCost int) *calllog.BillingMetadata {
	outcome, ok := billingOutcomeFromContext(ctx)
	if !ok {
		outcome = oneapi.BillingNotAttempted
	}
	return &calllog.BillingMetadata{Outcome: string(outcome), Price: baseCost,
		Charged: outcome.Charged(), Indeterminate: outcome.Indeterminate()}
}

// billingAttemptRecorded reports whether centralized billing was already invoked.
// Parameters:
//   - ctx: request context carrying billing attempt state.
//
// Returns:
//   - true when the invocation already hit centralized billing; otherwise false.
func billingAttemptRecorded(ctx context.Context) bool {
	tracker, ok := ctx.Value(billingAttemptContextKey{}).(*billingAttemptState)
	if !ok || tracker == nil {
		return false
	}

	return tracker.attempted
}

// reportMissingCentralizedBilling launches a zero-cost audit event when no prior billing call happened.
// Parameters:
//   - ctx: request context carrying billing attempt state.
//   - apiKey: request API key used for centralized billing auth.
//   - toolName: MCP tool name in snake_case.
func (s *Server) reportMissingCentralizedBilling(ctx context.Context, apiKey string, toolName string) {
	if s == nil || s.billingReporter == nil || apiKey == "" || toolName == "" || billingAttemptRecorded(ctx) {
		return
	}

	reportCtx := context.WithoutCancel(ctx)
	go func() {
		if err := s.billingReporter(reportCtx, apiKey, 0, toolName); err != nil {
			logger := s.logger
			if logger == nil {
				logger = log.Logger.Named("mcp")
			}

			logger.Warn("report tool usage to centralized billing",
				zap.Error(err),
				zap.String("tool", toolName),
			)
		}
	}()
}

// recordAndReportToolInvocation records local MCP call logs and emits centralized zero-cost audits when needed.
// Parameters:
//   - ctx: request context carrying billing attempt state.
//   - toolName: MCP tool name in snake_case.
//   - apiKey: request API key used for centralized billing auth.
//   - args: tool arguments for local call logging.
//   - startedAt: time when tool handling started.
//   - duration: elapsed tool execution time.
//   - baseCost: configured tool cost in OneAPI quota units.
//   - result: MCP tool result.
//   - invokeErr: Go error returned by the tool handler.
func (s *Server) recordAndReportToolInvocation(ctx context.Context, toolName string, apiKey string, args map[string]any, startedAt time.Time, duration time.Duration, baseCost int, result *mcp.CallToolResult, invokeErr error) {
	s.recordToolInvocation(ctx, toolName, apiKey, args, startedAt, duration, baseCost, result, invokeErr)
	s.reportMissingCentralizedBilling(ctx, apiKey, toolName)
}

// executeToolHandler runs a tool handler, then records both local and centralized billing logs.
// Parameters:
//   - ctx: request context for the MCP call.
//   - req: MCP call request.
//   - toolName: MCP tool name in snake_case.
//   - baseCost: configured tool cost in OneAPI quota units.
//   - unavailableMessage: user-facing error when the tool is not configured.
//   - exec: bound tool handler method or nil when the tool is unavailable.
//
// Returns:
//   - MCP tool result from the handler or an availability error result.
//   - wrapped Go error when the handler returned one.
func (s *Server) executeToolHandler(ctx context.Context, req mcp.CallToolRequest, toolName string, baseCost oneapi.Price, unavailableMessage string, exec toolExecutor) (*mcp.CallToolResult, error) {
	apiKey := apiKeyFromContext(ctx)
	args := argumentsMap(req.Params.Arguments)
	trackedCtx := withBillingAttemptTracking(ctx)

	if exec == nil {
		result := mcp.NewToolResultError(unavailableMessage)
		s.recordAndReportToolInvocation(trackedCtx, toolName, apiKey, args, time.Now().UTC(), 0, baseCost.Int(), result, nil)
		return result, nil
	}

	start := time.Now().UTC()
	result, err := exec(trackedCtx, req)
	duration := time.Since(start)
	s.recordAndReportToolInvocation(trackedCtx, toolName, apiKey, args, start, duration, baseCost.Int(), result, err)
	if err != nil {
		return result, errors.WithStack(err)
	}

	return result, nil
}

// LoggerFromContext retrieves the per-request logger from the MCP context.
// Falls back to a shared logger if none is present in context.
func LoggerFromContext(ctx context.Context) logSDK.Logger {
	if logger, ok := ctx.Value(ctxkeys.Logger).(logSDK.Logger); ok && logger != nil {
		return logger
	}
	if logger, ok := ctx.Value(keyLogger).(logSDK.Logger); ok && logger != nil {
		return logger
	}
	return log.Logger.Named("mcp_fallback")
}

// recordToolInvocation writes one local audit row for a tool invocation. The
// recorded cost follows the classified billing outcome rather than the tool
// result, so an accepted consume is not hidden by a later provider failure.
func (s *Server) recordToolInvocation(ctx context.Context, toolName string, apiKey string, args map[string]any, startedAt time.Time, duration time.Duration, baseCost int, result *mcp.CallToolResult, invokeErr error) {
	if s.callLogger == nil {
		if s.logger != nil {
			s.logger.Debug("call logger is nil, skipping record", zap.String("tool", toolName))
		}
		return
	}

	params := redactToolAuditParameters(toolName, args)
	status := calllog.StatusSuccess
	errorMessage := ""

	if invokeErr != nil {
		status = calllog.StatusError
		errorMessage = invokeErr.Error()
	}

	if result != nil && result.IsError {
		status = calllog.StatusError
		if msg := toolErrorMessage(result); msg != "" {
			if errorMessage == "" {
				errorMessage = msg
			} else {
				errorMessage = fmt.Sprintf("%s | %s", errorMessage, msg)
			}
		}
	}

	// The recorded cost follows the billing outcome, not the tool result: a
	// consume that was accepted spent the quota even when the provider then
	// failed, and recording zero there would hide a real charge.
	billing := billingMetadataForInvocation(ctx, baseCost)
	costValue := 0
	if billing.Charged {
		costValue = baseCost
	}

	if duration < 0 {
		duration = 0
	}

	occurredAt := startedAt.UTC()
	if startedAt.IsZero() {
		occurredAt = time.Now().UTC()
	}

	input := calllog.RecordInput{
		ToolName:     toolName,
		APIKey:       apiKey,
		Status:       status,
		Cost:         costValue,
		Duration:     duration,
		Parameters:   params,
		ErrorMessage: errorMessage,
		OccurredAt:   occurredAt,
		Billing:      billing,
	}

	if err := s.callLogger.Record(ctx, input); err != nil {
		logger := s.logger
		if logger == nil {
			logger = log.Logger.Named("mcp")
		}
		logger.Warn("record call log", zap.Error(err), zap.String("tool", toolName))
	}
}

// argumentsMap normalizes MCP tool arguments into a string-keyed map.
func argumentsMap(raw any) map[string]any {
	switch value := raw.(type) {
	case nil:
		return nil
	case map[string]any:
		return value
	case map[string]string:
		result := make(map[string]any, len(value))
		for key, item := range value {
			result[key] = item
		}
		return result
	default:
		return map[string]any{"value": value}
	}
}

// toolErrorMessage extracts the human-readable text of an error tool result.
func toolErrorMessage(result *mcp.CallToolResult) string {
	if result == nil {
		return ""
	}
	if !result.IsError {
		return ""
	}
	for _, content := range result.Content {
		if textContent, ok := mcp.AsTextContent(content); ok {
			txt := strings.TrimSpace(textContent.Text)
			if txt != "" {
				return txt
			}
		}
	}
	if result.StructuredContent != nil {
		return fmt.Sprint(result.StructuredContent)
	}
	return ""
}

// handleAskUser runs the ask_user tool through the shared audit wrapper.
func (s *Server) handleAskUser(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var exec toolExecutor
	if s.askUser != nil {
		exec = s.askUser.Handle
	}

	return s.executeToolHandler(ctx, req, "ask_user", 0, "ask_user tool is not available", exec)
}

// handleGetUserRequest runs the get_user_request tool through the shared audit wrapper.
func (s *Server) handleGetUserRequest(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var exec toolExecutor
	if s.getUserRequest != nil {
		exec = s.getUserRequest.Handle
	}

	return s.executeToolHandler(ctx, req, "get_user_request", 0, "get_user_request tool is not available", exec)
}

// handleExtractKeyInfo runs the extract_key_info tool through the shared audit wrapper.
func (s *Server) handleExtractKeyInfo(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var exec toolExecutor
	if s.extractKeyInfo != nil {
		exec = s.extractKeyInfo.Handle
	}

	return s.executeToolHandler(ctx, req, "extract_key_info", oneapi.PriceExtractKeyInfo, "extract_key_info tool is not available", exec)
}

// handleFileStat runs the file_stat tool through the shared audit wrapper.
func (s *Server) handleFileStat(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var exec toolExecutor
	if s.fileStat != nil {
		exec = s.fileStat.Handle
	}

	return s.executeToolHandler(ctx, req, "file_stat", 0, "file_stat tool is not available", exec)
}

// handleFileRead runs the file_read tool through the shared audit wrapper.
func (s *Server) handleFileRead(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var exec toolExecutor
	if s.fileRead != nil {
		exec = s.fileRead.Handle
	}

	return s.executeToolHandler(ctx, req, "file_read", 0, "file_read tool is not available", exec)
}

// handleFileWrite runs the file_write tool through the shared audit wrapper.
func (s *Server) handleFileWrite(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var exec toolExecutor
	if s.fileWrite != nil {
		exec = s.fileWrite.Handle
	}

	return s.executeToolHandler(ctx, req, "file_write", 0, "file_write tool is not available", exec)
}

// handleFileDelete runs the file_delete tool through the shared audit wrapper.
func (s *Server) handleFileDelete(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var exec toolExecutor
	if s.fileDelete != nil {
		exec = s.fileDelete.Handle
	}

	return s.executeToolHandler(ctx, req, "file_delete", 0, "file_delete tool is not available", exec)
}

// handleFileRename runs the file_rename tool through the shared audit wrapper.
func (s *Server) handleFileRename(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var exec toolExecutor
	if s.fileRename != nil {
		exec = s.fileRename.Handle
	}

	return s.executeToolHandler(ctx, req, "file_rename", 0, "file_rename tool is not available", exec)
}

// handleFileList runs the file_list tool through the shared audit wrapper.
func (s *Server) handleFileList(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var exec toolExecutor
	if s.fileList != nil {
		exec = s.fileList.Handle
	}

	return s.executeToolHandler(ctx, req, "file_list", 0, "file_list tool is not available", exec)
}

// handleFileSearch runs the file_search tool through the shared audit wrapper.
func (s *Server) handleFileSearch(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var exec toolExecutor
	if s.fileSearch != nil {
		exec = s.fileSearch.Handle
	}

	return s.executeToolHandler(ctx, req, "file_search", 0, "file_search tool is not available", exec)
}

// handleMCPPipe executes the mcp_pipe MCP tool, auditing the invocation via the call logger.
func (s *Server) handleMCPPipe(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var exec toolExecutor
	if s.mcpPipe != nil {
		exec = s.mcpPipe.Handle
	}

	return s.executeToolHandler(ctx, req, "mcp_pipe", 0, "mcp_pipe tool is not available", exec)
}

// handleFindTool executes the find_tool MCP tool, auditing the invocation via the call logger.
func (s *Server) handleFindTool(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var exec toolExecutor
	if s.findTool != nil {
		exec = s.findTool.Handle
	}

	return s.executeToolHandler(ctx, req, "find_tool", oneapi.PriceFindTool, "find_tool tool is not available", exec)
}

// handleMemoryBeforeTurn executes the memory_before_turn MCP tool and records call logs.
func (s *Server) handleMemoryBeforeTurn(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var exec toolExecutor
	if s.memoryBeforeTurn != nil {
		exec = s.memoryBeforeTurn.Handle
	}

	return s.executeToolHandler(ctx, req, "memory_before_turn", 0, "memory_before_turn tool is not available", exec)
}

// handleMemoryAfterTurn executes the memory_after_turn MCP tool and records call logs.
func (s *Server) handleMemoryAfterTurn(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var exec toolExecutor
	if s.memoryAfterTurn != nil {
		exec = s.memoryAfterTurn.Handle
	}

	return s.executeToolHandler(ctx, req, "memory_after_turn", 0, "memory_after_turn tool is not available", exec)
}

// handleMemoryRunMaintenance executes memory maintenance and records call logs.
func (s *Server) handleMemoryRunMaintenance(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var exec toolExecutor
	if s.memoryRunMaintenance != nil {
		exec = s.memoryRunMaintenance.Handle
	}

	return s.executeToolHandler(ctx, req, "memory_run_maintenance", 0, "memory_run_maintenance tool is not available", exec)
}

// handleMemoryListDirWithAbstract executes memory directory introspection and records call logs.
func (s *Server) handleMemoryListDirWithAbstract(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var exec toolExecutor
	if s.memoryListDirWithAbstract != nil {
		exec = s.memoryListDirWithAbstract.Handle
	}

	return s.executeToolHandler(ctx, req, "memory_list_dir_with_abstract", 0, "memory_list_dir_with_abstract tool is not available", exec)
}
