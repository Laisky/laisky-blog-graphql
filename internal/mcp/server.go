package mcp

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	errors "github.com/Laisky/errors/v2"
	gutils "github.com/Laisky/go-utils/v6"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	mcp "github.com/mark3labs/mcp-go/mcp"
	srv "github.com/mark3labs/mcp-go/server"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/askuser"
	mcpauth "github.com/Laisky/laisky-blog-graphql/internal/mcp/auth"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/calllog"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/ctxkeys"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
	mcpmemory "github.com/Laisky/laisky-blog-graphql/internal/mcp/memory"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/rag"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/tools"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/userrequests"
	"github.com/Laisky/laisky-blog-graphql/library/billing/oneapi"
	rlibs "github.com/Laisky/laisky-blog-graphql/library/db/redis"
	"github.com/Laisky/laisky-blog-graphql/library/log"
	searchlib "github.com/Laisky/laisky-blog-graphql/library/search"
)

type ctxKey string

const (
	keyAuthorization ctxKey = "authorization"
	keyLogger        ctxKey = "logger"
	httpLogBodyLimit int    = 4096
)

type callRecorder interface {
	Record(context.Context, calllog.RecordInput) error
}

// addToolWithSchemaValidation logs schema warnings and registers a tool handler.
func addToolWithSchemaValidation(mcpServer *srv.MCPServer, logger logSDK.Logger, definition mcp.Tool, handler srv.ToolHandlerFunc) {
	logInvalidArrayItemSchemas(logger, definition)
	mcpServer.AddTool(definition, handler)
}

// registerTool registers a tool on the MCP server and records its handler
// so that mcp_pipe can dynamically invoke any registered tool.
func (s *Server) registerTool(mcpServer *srv.MCPServer, definition mcp.Tool, handler srv.ToolHandlerFunc) {
	addToolWithSchemaValidation(mcpServer, s.logger, definition, handler)
	s.toolHandlers[definition.Name] = handler
	s.toolDefinitions[definition.Name] = definition
}

// getToolDefinition retrieves a registered tool definition by name.
func (s *Server) getToolDefinition(name string) (mcp.Tool, bool) {
	def, ok := s.toolDefinitions[name]
	return def, ok
}

// logInvalidArrayItemSchemas emits debug logs when an array input property has no items schema.
func logInvalidArrayItemSchemas(logger logSDK.Logger, definition mcp.Tool) {
	if logger == nil {
		return
	}

	for propertyName, propertyAny := range definition.InputSchema.Properties {
		property, ok := propertyAny.(map[string]any)
		if !ok {
			continue
		}

		if propertyType, _ := property["type"].(string); propertyType != "array" {
			continue
		}

		if _, ok := property["items"]; ok {
			continue
		}

		logger.Debug(
			"tool definition array property missing items schema",
			zap.String("tool", definition.Name),
			zap.String("property", propertyName),
		)
	}
}

// Server wraps the MCP server state for the HTTP transport.
type Server struct {
	handler                   http.Handler
	logger                    logSDK.Logger
	webSearch                 *tools.WebSearchTool
	webFetch                  *tools.WebFetchTool
	askUser                   *tools.AskUserTool
	getUserRequest            *tools.GetUserRequestTool
	extractKeyInfo            *tools.ExtractKeyInfoTool
	fileStat                  *tools.FileStatTool
	fileRead                  *tools.FileReadTool
	fileWrite                 *tools.FileWriteTool
	fileDelete                *tools.FileDeleteTool
	fileRename                *tools.FileRenameTool
	fileList                  *tools.FileListTool
	fileSearch                *tools.FileSearchTool
	memoryBeforeTurn          *tools.MemoryBeforeTurnTool
	memoryAfterTurn           *tools.MemoryAfterTurnTool
	memoryRunMaintenance      *tools.MemoryRunMaintenanceTool
	memoryListDirWithAbstract *tools.MemoryListDirWithAbstractTool
	mcpPipe                   *tools.MCPPipeTool
	findTool                  *tools.FindToolTool
	callLogger                callRecorder
	holdManager               *userrequests.HoldManager
	// toolHandlers maps tool names to their handler functions,
	// enabling mcp_pipe to dynamically invoke any registered tool.
	toolHandlers    map[string]srv.ToolHandlerFunc
	toolDefinitions map[string]mcp.Tool
}

// NewServer constructs an MCP HTTP server.
// searchProvider enables the web_search tool when not nil and toolsSettings.WebSearchEnabled is true.
// askUserService enables the ask_user tool when not nil and toolsSettings.AskUserEnabled is true.
// rdb enables the web_fetch tool when not nil and toolsSettings.WebFetchEnabled is true.
// userRequestService enables the get_user_request tool when not nil and toolsSettings.GetUserRequestEnabled is true.
// ragService enables the extract_key_info tool when not nil and toolsSettings.ExtractKeyInfoEnabled is true.
// callLogger records tool invocations for auditing when provided.
// logger overrides the default logger when provided.
// It returns the configured server or an error if no capability is available.
func NewServer(searchProvider searchlib.Provider, askUserService *askuser.Service, userRequestService *userrequests.Service, ragService *rag.Service, ragSettings rag.Settings, fileService *files.Service, memoryService *mcpmemory.Service, rdb *rlibs.DB, callLogger callRecorder, toolsSettings ToolsSettings, logger logSDK.Logger) (*Server, error) {
	if searchProvider == nil && askUserService == nil && userRequestService == nil && ragService == nil && fileService == nil && memoryService == nil && rdb == nil && !toolsSettings.MCPPipeEnabled && !toolsSettings.FindToolEnabled {
		return nil, errors.New("at least one MCP capability must be enabled")
	}
	if logger == nil {
		logger = log.Logger
	}

	hooks := newMCPHooks(logger.Named("mcp_hooks"))

	mcpServer := srv.NewMCPServer(
		"LAISKY MCP SERVER",
		"1.0.0",
		srv.WithToolCapabilities(true),
		srv.WithInstructions("Use web_search for Google Programmable Search queries and web_fetch to retrieve dynamic web pages."),
		srv.WithRecovery(),
		srv.WithHooks(hooks),
	)

	serverLogger := logger.Named("mcp")

	streamable := srv.NewStreamableHTTPServer(
		mcpServer,
		srv.WithHTTPContextFunc(func(ctx context.Context, r *http.Request) context.Context {
			// Inject authorization header with backward-compatible query fallback.
			authHeader, authSource := resolveRequestAuthorizationHeader(r)
			ctx = context.WithValue(ctx, keyAuthorization, authHeader)
			if authCtx, err := mcpauth.ParseAuthorizationContext(authHeader); err == nil {
				ctx = mcpauth.WithContext(ctx, authCtx)
			} else if authHeader != "" {
				serverLogger.Debug("mcp authorization parse failed",
					zap.String("auth_source", authSource),
					zap.Error(err),
				)
			}
			if authSource != "header" {
				serverLogger.Debug("mcp authorization source resolved",
					zap.String("auth_source", authSource),
					zap.Bool("has_session_header", strings.TrimSpace(r.Header.Get(srv.HeaderKeySessionID)) != ""),
				)
			}

			// Create a per-request logger with request-specific context
			reqLogger := serverLogger.With(
				zap.String("request_id", gutils.UUID7()),
				zap.String("remote_addr", r.RemoteAddr),
				zap.String("method", r.Method),
				zap.String("path", r.URL.Path),
			)
			ctx = context.WithValue(ctx, keyLogger, reqLogger)
			ctx = context.WithValue(ctx, ctxkeys.Logger, reqLogger)
			return ctx
		}),
	)

	normalizedAuthHandler := withAuthorizationHeaderNormalization(streamable, serverLogger.Named("auth"))
	s := &Server{
		handler:      withHTTPLogging(withToolsListFiltering(normalizedAuthHandler, serverLogger.Named("tools_list_filter"), userRequestService), serverLogger.Named("http")),
		logger:       serverLogger,
		callLogger:   callLogger,
		toolHandlers:    make(map[string]srv.ToolHandlerFunc),
		toolDefinitions: make(map[string]mcp.Tool),
	}

	apiKeyProvider := func(ctx context.Context) string {
		return apiKeyFromContext(ctx)
	}
	headerProvider := func(ctx context.Context) string {
		authHeader, _ := ctx.Value(keyAuthorization).(string)
		return authHeader
	}

	if searchProvider != nil && toolsSettings.WebSearchEnabled {
		webSearchTool, err := tools.NewWebSearchTool(
			searchProvider,
			serverLogger.Named("web_search"),
			apiKeyProvider,
			oneapi.CheckUserExternalBilling,
		)
		if err != nil {
			return nil, errors.Wrap(err, "init web_search tool")
		}
		s.webSearch = webSearchTool
		s.registerTool(mcpServer,webSearchTool.Definition(), s.handleWebSearch)
	} else if searchProvider != nil && !toolsSettings.WebSearchEnabled {
		serverLogger.Info("web_search tool disabled by configuration")
	}

	if rdb != nil && toolsSettings.WebFetchEnabled {
		webFetchTool, err := tools.NewWebFetchTool(
			rdb,
			serverLogger.Named("web_fetch"),
			apiKeyProvider,
			oneapi.CheckUserExternalBilling,
			searchlib.FetchDynamicURLContent,
		)
		if err != nil {
			return nil, errors.Wrap(err, "init web_fetch tool")
		}
		s.webFetch = webFetchTool
		s.registerTool(mcpServer,webFetchTool.Definition(), s.handleWebFetch)
	} else if rdb != nil && !toolsSettings.WebFetchEnabled {
		serverLogger.Info("web_fetch tool disabled by configuration")
	}

	if askUserService != nil && toolsSettings.AskUserEnabled {
		askUserTool, err := tools.NewAskUserTool(
			askUserService,
			serverLogger.Named("ask_user"),
			headerProvider,
			askuser.ParseAuthorizationContext,
			0,
		)
		if err != nil {
			return nil, errors.Wrap(err, "init ask_user tool")
		}
		s.askUser = askUserTool
		s.registerTool(mcpServer,askUserTool.Definition(), s.handleAskUser)
	} else if askUserService != nil && !toolsSettings.AskUserEnabled {
		serverLogger.Info("ask_user tool disabled by configuration")
	}

	if userRequestService != nil && toolsSettings.GetUserRequestEnabled {
		// Create HoldManager for this user request service
		holdMgr := userrequests.NewHoldManager(userRequestService, serverLogger.Named("hold_manager"), nil)
		s.holdManager = holdMgr

		getUserRequestTool, err := tools.NewGetUserRequestTool(
			userRequestService,
			holdMgr,
			serverLogger.Named("get_user_request"),
			headerProvider,
			askuser.ParseAuthorizationContext,
		)
		if err != nil {
			return nil, errors.Wrap(err, "init get_user_request tool")
		}
		s.getUserRequest = getUserRequestTool
		s.registerTool(mcpServer,getUserRequestTool.Definition(), s.handleGetUserRequest)
	} else if userRequestService != nil && !toolsSettings.GetUserRequestEnabled {
		serverLogger.Info("get_user_request tool disabled by configuration")
	}

	if ragService != nil && toolsSettings.ExtractKeyInfoEnabled {
		ragTool, err := tools.NewExtractKeyInfoTool(
			ragService,
			serverLogger.Named("extract_key_info"),
			headerProvider,
			oneapi.CheckUserExternalBilling,
			ragSettings,
		)
		if err != nil {
			return nil, errors.Wrap(err, "init extract_key_info tool")
		}
		s.extractKeyInfo = ragTool
		s.registerTool(mcpServer,ragTool.Definition(), s.handleExtractKeyInfo)
	} else if ragService != nil && !toolsSettings.ExtractKeyInfoEnabled {
		serverLogger.Info("extract_key_info tool disabled by configuration")
	}

	if fileService != nil && toolsSettings.FileIOEnabled {
		fileStatTool, err := tools.NewFileStatTool(fileService)
		if err != nil {
			return nil, errors.Wrap(err, "init file_stat tool")
		}
		s.fileStat = fileStatTool
		s.registerTool(mcpServer,fileStatTool.Definition(), s.handleFileStat)

		fileReadTool, err := tools.NewFileReadTool(fileService)
		if err != nil {
			return nil, errors.Wrap(err, "init file_read tool")
		}
		s.fileRead = fileReadTool
		s.registerTool(mcpServer,fileReadTool.Definition(), s.handleFileRead)

		fileWriteTool, err := tools.NewFileWriteTool(fileService)
		if err != nil {
			return nil, errors.Wrap(err, "init file_write tool")
		}
		s.fileWrite = fileWriteTool
		s.registerTool(mcpServer,fileWriteTool.Definition(), s.handleFileWrite)

		fileDeleteTool, err := tools.NewFileDeleteTool(fileService)
		if err != nil {
			return nil, errors.Wrap(err, "init file_delete tool")
		}
		s.fileDelete = fileDeleteTool
		s.registerTool(mcpServer,fileDeleteTool.Definition(), s.handleFileDelete)

		fileRenameTool, err := tools.NewFileRenameTool(fileService)
		if err != nil {
			return nil, errors.Wrap(err, "init file_rename tool")
		}
		s.fileRename = fileRenameTool
		s.registerTool(mcpServer,fileRenameTool.Definition(), s.handleFileRename)

		fileListTool, err := tools.NewFileListTool(fileService)
		if err != nil {
			return nil, errors.Wrap(err, "init file_list tool")
		}
		s.fileList = fileListTool
		s.registerTool(mcpServer,fileListTool.Definition(), s.handleFileList)

		fileSearchTool, err := tools.NewFileSearchTool(fileService)
		if err != nil {
			return nil, errors.Wrap(err, "init file_search tool")
		}
		s.fileSearch = fileSearchTool
		s.registerTool(mcpServer,fileSearchTool.Definition(), s.handleFileSearch)
	} else if fileService != nil && !toolsSettings.FileIOEnabled {
		serverLogger.Info("file tools disabled by configuration")
	}

	if memoryService != nil && toolsSettings.MemoryEnabled {
		memoryBeforeTurnTool, err := tools.NewMemoryBeforeTurnTool(memoryService)
		if err != nil {
			return nil, errors.Wrap(err, "init memory_before_turn tool")
		}
		s.memoryBeforeTurn = memoryBeforeTurnTool
		s.registerTool(mcpServer,memoryBeforeTurnTool.Definition(), s.handleMemoryBeforeTurn)

		memoryAfterTurnTool, err := tools.NewMemoryAfterTurnTool(memoryService)
		if err != nil {
			return nil, errors.Wrap(err, "init memory_after_turn tool")
		}
		s.memoryAfterTurn = memoryAfterTurnTool
		s.registerTool(mcpServer,memoryAfterTurnTool.Definition(), s.handleMemoryAfterTurn)

		memoryMaintenanceTool, err := tools.NewMemoryRunMaintenanceTool(memoryService)
		if err != nil {
			return nil, errors.Wrap(err, "init memory_run_maintenance tool")
		}
		s.memoryRunMaintenance = memoryMaintenanceTool
		s.registerTool(mcpServer,memoryMaintenanceTool.Definition(), s.handleMemoryRunMaintenance)

		memoryListTool, err := tools.NewMemoryListDirWithAbstractTool(memoryService)
		if err != nil {
			return nil, errors.Wrap(err, "init memory_list_dir_with_abstract tool")
		}
		s.memoryListDirWithAbstract = memoryListTool
		s.registerTool(mcpServer,memoryListTool.Definition(), s.handleMemoryListDirWithAbstract)
	} else if memoryService != nil && !toolsSettings.MemoryEnabled {
		serverLogger.Info("memory tools disabled by configuration")
	}

	if toolsSettings.MCPPipeEnabled {
		pipeTool, err := tools.NewMCPPipeTool(
			serverLogger.Named("mcp_pipe"),
			func(ctx context.Context, toolName string, args any) (*mcp.CallToolResult, error) {
				// Prevent direct recursion; nested pipelines should use the 'pipe' step type.
				if toolName == "mcp_pipe" {
					return mcp.NewToolResultError("mcp_pipe cannot invoke itself"), nil
				}

				handler, ok := s.toolHandlers[toolName]
				if !ok {
					return mcp.NewToolResultError(fmt.Sprintf("unknown tool: %s", toolName)), nil
				}

				req := mcp.CallToolRequest{Params: mcp.CallToolParams{Name: toolName, Arguments: args}}
				return handler(ctx, req)
			},
			tools.PipeLimits{},
		)
		if err != nil {
			return nil, errors.Wrap(err, "init mcp_pipe tool")
		}
		s.mcpPipe = pipeTool
		s.registerTool(mcpServer,pipeTool.Definition(), s.handleMCPPipe)
	} else {
		serverLogger.Info("mcp_pipe tool disabled by configuration")
	}

	if toolsSettings.FindToolEnabled && ragSettings.OpenAIBaseURL != "" && ragSettings.EmbeddingModel != "" {
		embedder := rag.NewOpenAIEmbedder(ragSettings.OpenAIBaseURL, ragSettings.EmbeddingModel, nil,
			rag.WithLogger(serverLogger.Named("embedder")))
		findToolInstance, err := tools.NewFindToolTool(
			embedder,
			serverLogger.Named("find_tool"),
			headerProvider,
			oneapi.CheckUserExternalBilling,
			ragSettings,
		)
		if err != nil {
			return nil, errors.Wrap(err, "init find_tool tool")
		}
		s.findTool = findToolInstance
		s.registerTool(mcpServer, findToolInstance.Definition(), s.handleFindTool)

		// Collect all registered tool definitions (excluding find_tool itself)
		// and provide them to the find_tool for indexing.
		allTools := make([]mcp.Tool, 0, len(s.toolDefinitions))
		for name, def := range s.toolDefinitions {
			if name == "find_tool" {
				continue
			}
			allTools = append(allTools, def)
		}
		findToolInstance.SetTools(allTools)
	} else if toolsSettings.FindToolEnabled {
		serverLogger.Info("find_tool disabled: missing embedding configuration")
	}

	return s, nil
}

// Handler returns the HTTP handler that should be mounted to serve MCP traffic.
func (s *Server) Handler() http.Handler {
	return s.handler
}

// HoldManager returns the HoldManager instance for the user requests feature.
// Returns nil if the get_user_request tool is not enabled.
func (s *Server) HoldManager() *userrequests.HoldManager {
	return s.holdManager
}

// AvailableToolNames returns all MCP tool names currently registered by the server.
func (s *Server) AvailableToolNames() []string {
	if s == nil {
		return []string{}
	}

	toolNames := make([]string, 0, len(s.toolHandlers))
	for name := range s.toolHandlers {
		toolNames = append(toolNames, name)
	}

	sort.Strings(toolNames)
	return toolNames
}
