package mcp

import (
	"context"
	"fmt"
	"net/http"
	"sort"

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
	callLogger                callRecorder
	holdManager               *userrequests.HoldManager
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
	if searchProvider == nil && askUserService == nil && userRequestService == nil && ragService == nil && fileService == nil && memoryService == nil && rdb == nil && !toolsSettings.MCPPipeEnabled {
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
			// Inject authorization header
			authHeader := r.Header.Get("Authorization")
			ctx = context.WithValue(ctx, keyAuthorization, authHeader)
			if authCtx, err := mcpauth.ParseAuthorizationContext(authHeader); err == nil {
				ctx = mcpauth.WithContext(ctx, authCtx)
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

	s := &Server{
		handler:    withHTTPLogging(withToolsListFiltering(streamable, serverLogger.Named("tools_list_filter"), userRequestService), serverLogger.Named("http")),
		logger:     serverLogger,
		callLogger: callLogger,
	}

	apiKeyProvider := func(ctx context.Context) string {
		authHeader, _ := ctx.Value(keyAuthorization).(string)
		return extractAPIKey(authHeader)
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
		mcpServer.AddTool(webSearchTool.Definition(), s.handleWebSearch)
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
		mcpServer.AddTool(webFetchTool.Definition(), s.handleWebFetch)
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
		mcpServer.AddTool(askUserTool.Definition(), s.handleAskUser)
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
		mcpServer.AddTool(getUserRequestTool.Definition(), s.handleGetUserRequest)
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
		mcpServer.AddTool(ragTool.Definition(), s.handleExtractKeyInfo)
	} else if ragService != nil && !toolsSettings.ExtractKeyInfoEnabled {
		serverLogger.Info("extract_key_info tool disabled by configuration")
	}

	if fileService != nil && toolsSettings.FileIOEnabled {
		fileStatTool, err := tools.NewFileStatTool(fileService)
		if err != nil {
			return nil, errors.Wrap(err, "init file_stat tool")
		}
		s.fileStat = fileStatTool
		mcpServer.AddTool(fileStatTool.Definition(), s.handleFileStat)

		fileReadTool, err := tools.NewFileReadTool(fileService)
		if err != nil {
			return nil, errors.Wrap(err, "init file_read tool")
		}
		s.fileRead = fileReadTool
		mcpServer.AddTool(fileReadTool.Definition(), s.handleFileRead)

		fileWriteTool, err := tools.NewFileWriteTool(fileService)
		if err != nil {
			return nil, errors.Wrap(err, "init file_write tool")
		}
		s.fileWrite = fileWriteTool
		mcpServer.AddTool(fileWriteTool.Definition(), s.handleFileWrite)

		fileDeleteTool, err := tools.NewFileDeleteTool(fileService)
		if err != nil {
			return nil, errors.Wrap(err, "init file_delete tool")
		}
		s.fileDelete = fileDeleteTool
		mcpServer.AddTool(fileDeleteTool.Definition(), s.handleFileDelete)

		fileRenameTool, err := tools.NewFileRenameTool(fileService)
		if err != nil {
			return nil, errors.Wrap(err, "init file_rename tool")
		}
		s.fileRename = fileRenameTool
		mcpServer.AddTool(fileRenameTool.Definition(), s.handleFileRename)

		fileListTool, err := tools.NewFileListTool(fileService)
		if err != nil {
			return nil, errors.Wrap(err, "init file_list tool")
		}
		s.fileList = fileListTool
		mcpServer.AddTool(fileListTool.Definition(), s.handleFileList)

		fileSearchTool, err := tools.NewFileSearchTool(fileService)
		if err != nil {
			return nil, errors.Wrap(err, "init file_search tool")
		}
		s.fileSearch = fileSearchTool
		mcpServer.AddTool(fileSearchTool.Definition(), s.handleFileSearch)
	} else if fileService != nil && !toolsSettings.FileIOEnabled {
		serverLogger.Info("file tools disabled by configuration")
	}

	if memoryService != nil && toolsSettings.MemoryEnabled {
		memoryBeforeTurnTool, err := tools.NewMemoryBeforeTurnTool(memoryService)
		if err != nil {
			return nil, errors.Wrap(err, "init memory_before_turn tool")
		}
		s.memoryBeforeTurn = memoryBeforeTurnTool
		mcpServer.AddTool(memoryBeforeTurnTool.Definition(), s.handleMemoryBeforeTurn)

		memoryAfterTurnTool, err := tools.NewMemoryAfterTurnTool(memoryService)
		if err != nil {
			return nil, errors.Wrap(err, "init memory_after_turn tool")
		}
		s.memoryAfterTurn = memoryAfterTurnTool
		mcpServer.AddTool(memoryAfterTurnTool.Definition(), s.handleMemoryAfterTurn)

		memoryMaintenanceTool, err := tools.NewMemoryRunMaintenanceTool(memoryService)
		if err != nil {
			return nil, errors.Wrap(err, "init memory_run_maintenance tool")
		}
		s.memoryRunMaintenance = memoryMaintenanceTool
		mcpServer.AddTool(memoryMaintenanceTool.Definition(), s.handleMemoryRunMaintenance)

		memoryListTool, err := tools.NewMemoryListDirWithAbstractTool(memoryService)
		if err != nil {
			return nil, errors.Wrap(err, "init memory_list_dir_with_abstract tool")
		}
		s.memoryListDirWithAbstract = memoryListTool
		mcpServer.AddTool(memoryListTool.Definition(), s.handleMemoryListDirWithAbstract)
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

				req := mcp.CallToolRequest{Params: mcp.CallToolParams{Name: toolName, Arguments: args}}
				switch toolName {
				case "web_search":
					return s.handleWebSearch(ctx, req)
				case "web_fetch":
					return s.handleWebFetch(ctx, req)
				case "ask_user":
					return s.handleAskUser(ctx, req)
				case "get_user_request":
					return s.handleGetUserRequest(ctx, req)
				case "extract_key_info":
					return s.handleExtractKeyInfo(ctx, req)
				case "file_stat":
					return s.handleFileStat(ctx, req)
				case "file_read":
					return s.handleFileRead(ctx, req)
				case "file_write":
					return s.handleFileWrite(ctx, req)
				case "file_delete":
					return s.handleFileDelete(ctx, req)
				case "file_rename":
					return s.handleFileRename(ctx, req)
				case "file_list":
					return s.handleFileList(ctx, req)
				case "file_search":
					return s.handleFileSearch(ctx, req)
				case "memory_before_turn":
					return s.handleMemoryBeforeTurn(ctx, req)
				case "memory_after_turn":
					return s.handleMemoryAfterTurn(ctx, req)
				case "memory_run_maintenance":
					return s.handleMemoryRunMaintenance(ctx, req)
				case "memory_list_dir_with_abstract":
					return s.handleMemoryListDirWithAbstract(ctx, req)
				default:
					return mcp.NewToolResultError(fmt.Sprintf("unknown tool: %s", toolName)), nil
				}
			},
			tools.PipeLimits{},
		)
		if err != nil {
			return nil, errors.Wrap(err, "init mcp_pipe tool")
		}
		s.mcpPipe = pipeTool
		mcpServer.AddTool(pipeTool.Definition(), s.handleMCPPipe)
	} else {
		serverLogger.Info("mcp_pipe tool disabled by configuration")
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

	toolNames := make([]string, 0, 16)
	if s.webSearch != nil {
		toolNames = append(toolNames, "web_search")
	}
	if s.webFetch != nil {
		toolNames = append(toolNames, "web_fetch")
	}
	if s.askUser != nil {
		toolNames = append(toolNames, "ask_user")
	}
	if s.getUserRequest != nil {
		toolNames = append(toolNames, "get_user_request")
	}
	if s.extractKeyInfo != nil {
		toolNames = append(toolNames, "extract_key_info")
	}
	if s.fileStat != nil {
		toolNames = append(toolNames, "file_stat")
	}
	if s.fileRead != nil {
		toolNames = append(toolNames, "file_read")
	}
	if s.fileWrite != nil {
		toolNames = append(toolNames, "file_write")
	}
	if s.fileDelete != nil {
		toolNames = append(toolNames, "file_delete")
	}
	if s.fileRename != nil {
		toolNames = append(toolNames, "file_rename")
	}
	if s.fileList != nil {
		toolNames = append(toolNames, "file_list")
	}
	if s.fileSearch != nil {
		toolNames = append(toolNames, "file_search")
	}
	if s.memoryBeforeTurn != nil {
		toolNames = append(toolNames, "memory_before_turn")
	}
	if s.memoryAfterTurn != nil {
		toolNames = append(toolNames, "memory_after_turn")
	}
	if s.memoryRunMaintenance != nil {
		toolNames = append(toolNames, "memory_run_maintenance")
	}
	if s.memoryListDirWithAbstract != nil {
		toolNames = append(toolNames, "memory_list_dir_with_abstract")
	}
	if s.mcpPipe != nil {
		toolNames = append(toolNames, "mcp_pipe")
	}

	sort.Strings(toolNames)
	return toolNames
}
