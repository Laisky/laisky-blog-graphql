// Package web gin server
package web

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/99designs/gqlgen/graphql"
	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/extension"
	"github.com/99designs/gqlgen/graphql/handler/transport"
	"github.com/99designs/gqlgen/graphql/playground"
	ginMw "github.com/Laisky/gin-middlewares/v7"
	gconfig "github.com/Laisky/go-config/v2"
	glog "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
	"github.com/gin-gonic/gin"
	"github.com/vektah/gqlparser/v2/gqlerror"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/askuser"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/calllog"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/userrequests"
	"github.com/Laisky/laisky-blog-graphql/library/log"
)

var (
	server = gin.New()
)

const defaultURLPrefix = "/mcp"

type urlPrefixConfig struct {
	internal string
	public   string
}

func newURLPrefixConfig() urlPrefixConfig {
	rawInternal := strings.TrimSpace(gconfig.Shared.GetString("settings.web.url_prefix"))
	internal := normalizeBasePath(rawInternal)
	if rawInternal == "" && internal == "" {
		internal = normalizeBasePath(defaultURLPrefix)
	}

	rawPublic := strings.TrimSpace(gconfig.Shared.GetString("settings.web.public_url_prefix"))
	public := normalizeBasePath(rawPublic)
	if rawPublic == "" {
		public = internal
	}

	return urlPrefixConfig{internal: internal, public: public}
}

func (c urlPrefixConfig) join(path string) string {
	if path == "" {
		if c.internal == "" {
			return "/"
		}
		return c.internal
	}

	if path == "/" {
		if c.internal == "" {
			return "/"
		}
		return c.internal + "/"
	}

	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if c.internal == "" {
		return path
	}
	return c.internal + path
}

func (c urlPrefixConfig) rootVariants() []string {
	add := func(set map[string]struct{}, values ...string) {
		for _, v := range values {
			if v == "" {
				v = "/"
			}
			set[v] = struct{}{}
		}
	}

	paths := make(map[string]struct{})
	if c.internal == "" {
		add(paths, "/")
	} else {
		add(paths, c.internal, c.internal+"/")
	}

	if c.public == "" {
		add(paths, "/")
	} else if c.public != c.internal {
		add(paths, c.public, c.public+"/")
	}

	result := make([]string, 0, len(paths))
	for path := range paths {
		result = append(result, path)
	}

	return result
}

func (c urlPrefixConfig) matches(path string) bool {
	if path == "" || !strings.HasPrefix(path, "/") {
		return false
	}

	check := func(base string) bool {
		if base == "" {
			return true
		}
		if path == base {
			return true
		}
		return strings.HasPrefix(path, base+"/")
	}

	if check(c.internal) {
		return true
	}
	if c.public != c.internal && check(c.public) {
		return true
	}

	return false
}

func (c urlPrefixConfig) display(value string) string {
	if value == "" {
		return "/"
	}
	return value
}

func RunServer(addr string, resolver *Resolver) {
	prefix := newURLPrefixConfig()
	log.Logger.Info("configuring web url prefix",
		zap.String("internal_prefix", prefix.display(prefix.internal)),
		zap.String("public_prefix", prefix.display(prefix.public)),
	)
	siteConfigs := loadSiteConfigSet(log.Logger.Named("site_config"), prefix)

	gin.SetMode(gin.ReleaseMode)
	if gconfig.Shared.GetBool("debug") {
		err := log.Logger.ChangeLevel(glog.LevelDebug)
		if err != nil {
			log.Logger.Panic("change logger level", zap.Error(err))
		}

		log.Logger.Info("debug mode enabled")
	}

	frontendSPA := newFrontendSPAHandler(log.Logger.Named("frontend_spa"), prefix.internal)
	server.Use(
		gin.Recovery(),
		ginMw.NewLoggerMiddleware(
			ginMw.WithLoggerMwColored(),
			ginMw.WithLevel(log.Logger.Level().String()),
			ginMw.WithLogger(log.Logger.Named("gin")),
		),
		allowCORS,
	)

	if err := ginMw.EnableMetric(server); err != nil {
		log.Logger.Panic("enable metric server", zap.Error(err))
	}

	registerOneapiProxyRoutes(server, prefix)

	if resolver != nil && (resolver.args.WebSearchProvider != nil || resolver.args.AskUserService != nil || resolver.args.UserRequestService != nil || resolver.args.RAGService != nil || resolver.args.FilesService != nil) {
		mcpServer, err := mcp.NewServer(resolver.args.WebSearchProvider, resolver.args.AskUserService, resolver.args.UserRequestService, resolver.args.RAGService, resolver.args.RAGSettings, resolver.args.FilesService, resolver.args.Rdb, resolver.args.CallLogService, resolver.args.MCPToolsSettings, log.Logger)
		if err != nil {
			log.Logger.Error("init mcp server", zap.Error(err))
		} else {
			mcpHandler := mcpServer.Handler()
			rootHandler := func(ctx *gin.Context) {
				if frontendSPA != nil && shouldServeFrontend(ctx.Request) {
					frontendSPA.ServeHTTP(ctx.Writer, ctx.Request)
					return
				}
				mcpHandler.ServeHTTP(ctx.Writer, ctx.Request)
			}
			for _, rootPath := range prefix.rootVariants() {
				server.Any(rootPath, rootHandler)
			}

			if resolver.args.AskUserService != nil {
				askUserMux := askuser.NewHTTPHandler(resolver.args.AskUserService, log.Logger.Named("ask_user_http"))
				askUserBase := prefix.join("/tools/ask_user")
				askUserHandler := gin.WrapH(askUserMux)
				server.Any(askUserBase, askUserHandler)
				askUserWildcard := prefix.join("/tools/ask_user/*path")
				stripPrefix := strings.TrimSuffix(askUserBase, "/")
				if stripPrefix == "" {
					stripPrefix = "/"
				}
				server.Any(askUserWildcard, gin.WrapH(http.StripPrefix(stripPrefix, askUserMux)))
				if prefix.public == "" {
					server.Any("/tools/ask_user", askUserHandler)
					server.Any("/tools/ask_user/*path", gin.WrapH(http.StripPrefix("/tools/ask_user", askUserMux)))
				}
			}

			if resolver.args.CallLogService != nil {
				callLogMux := calllog.NewHTTPHandler(resolver.args.CallLogService, log.Logger.Named("call_log_http"))
				callLogBase := prefix.join("/tools/call_log")
				stripPrefix := strings.TrimSuffix(callLogBase, "/")
				if stripPrefix == "" {
					stripPrefix = "/"
				}
				callLogHandler := gin.WrapH(http.StripPrefix(stripPrefix, callLogMux))

				apiBase := prefix.join("/tools/call_log/api")
				server.Any(apiBase, callLogHandler)
				server.Any(apiBase+"/*path", callLogHandler)

				if prefix.public == "" {
					server.Any("/tools/call_log/api", gin.WrapH(http.StripPrefix("/tools/call_log", callLogMux)))
					server.Any("/tools/call_log/api/*path", gin.WrapH(http.StripPrefix("/tools/call_log", callLogMux)))
				}
			}

			if resolver.args.UserRequestService != nil {
				// Combined handler that routes to either user requests or saved commands based on path
				// The HoldManager is obtained from the MCP server to share state with the get_user_request tool
				combinedMux := userrequests.NewCombinedHTTPHandler(resolver.args.UserRequestService, mcpServer.HoldManager(), log.Logger.Named("user_requests_http"))
				userReqBase := prefix.join("/tools/get_user_requests")
				stripPrefix := strings.TrimSuffix(userReqBase, "/")
				if stripPrefix == "" {
					stripPrefix = "/"
				}
				userReqHandler := gin.WrapH(http.StripPrefix(stripPrefix, combinedMux))

				apiBase := prefix.join("/tools/get_user_requests/api")
				server.Any(apiBase, userReqHandler)
				server.Any(apiBase+"/*path", userReqHandler)

				if prefix.public == "" {
					server.Any("/tools/get_user_requests/api", gin.WrapH(http.StripPrefix("/tools/get_user_requests", combinedMux)))
					server.Any("/tools/get_user_requests/api/*path", gin.WrapH(http.StripPrefix("/tools/get_user_requests", combinedMux)))
				}
			}
		}
	} else {
		searchNil := resolver != nil && resolver.args.WebSearchProvider == nil
		askNil := resolver != nil && resolver.args.AskUserService == nil
		userReqNil := resolver != nil && resolver.args.UserRequestService == nil
		log.Logger.Warn("skip mcp server initialization",
			zap.Bool("resolver_nil", resolver == nil),
			zap.Bool("web_search_provider_nil", searchNil),
			zap.Bool("ask_user_service_nil", askNil),
			zap.Bool("user_request_service_nil", userReqNil),
			zap.String("internal_prefix", prefix.display(prefix.internal)),
		)
	}

	server.Any("/health", func(ctx *gin.Context) {
		ctx.String(http.StatusOK, "hello, world")
	})

	statusHandler := newStatusHandler()
	statusPath := prefix.join("/status")
	server.GET(statusPath, statusHandler)
	server.HEAD(statusPath, statusHandler)
	server.OPTIONS(statusPath, statusHandler)
	if trimmed := strings.TrimSuffix(statusPath, "/"); trimmed != statusPath {
		server.GET(trimmed, statusHandler)
		server.HEAD(trimmed, statusHandler)
		server.OPTIONS(trimmed, statusHandler)
	}
	if prefix.public == "" {
		server.GET("/status", statusHandler)
		server.HEAD("/status", statusHandler)
		server.OPTIONS("/status", statusHandler)
	}

	h := handler.New(NewExecutableSchema(Config{Resolvers: resolver}))
	h.AddTransport(transport.Websocket{
		KeepAlivePingInterval: 10 * time.Second,
	})
	h.AddTransport(transport.GET{})
	h.AddTransport(transport.POST{})
	h.AddTransport(transport.Options{})
	h.AddTransport(transport.MultipartForm{})
	h.Use(extension.Introspection{})
	h.SetErrorPresenter(func(ctx context.Context, e error) *gqlerror.Error {
		err := graphql.DefaultErrorPresenter(ctx, e)

		// there are huge of junk logs about "alert token invalidate",
		// so we just ignore it
		errMsg := e.Error()
		if !strings.Contains(errMsg, "token invalidate for ") &&
			!strings.Contains(errMsg, "ValidateTokenForAlertType") &&
			!strings.Contains(errMsg, "deny by throttle") {
			// gqlgen will wrap origin error, that will make error stack trace lost,
			// so we need to unwrap it and log the origin error.
			log.Logger.Error("graphql server", zap.Error(err.Err))
		}

		return err
	})

	graphqlHandler := ginMw.FromStd(h.ServeHTTP)
	queryPath := prefix.join("/query/")
	server.Any(queryPath, graphqlHandler)
	if trimmed := strings.TrimSuffix(queryPath, "/"); trimmed != queryPath {
		server.Any(trimmed, graphqlHandler)
	}
	if prefix.public == "" {
		server.Any("/query/", graphqlHandler)
		server.Any("/query", graphqlHandler)
	}

	queryV2Path := prefix.join("/query/v2/")
	server.Any(queryV2Path, graphqlHandler)
	if trimmed := strings.TrimSuffix(queryV2Path, "/"); trimmed != queryV2Path {
		server.Any(trimmed, graphqlHandler)
	}
	if prefix.public == "" {
		server.Any("/query/v2/", graphqlHandler)
		server.Any("/query/v2", graphqlHandler)
	}

	playgroundHandler := ginMw.FromStd(playground.Handler("GraphQL playground", queryPath))
	uiPath := prefix.join("/ui/")
	server.Any(uiPath, playgroundHandler)
	if trimmed := strings.TrimSuffix(uiPath, "/"); trimmed != uiPath {
		server.Any(trimmed, playgroundHandler)
	}
	if prefix.public == "" {
		server.Any("/ui/", playgroundHandler)
		server.Any("/ui", playgroundHandler)
	}

	runtimeConfigHandler := func(ctx *gin.Context) {
		logger := ginMw.GetLogger(ctx).Named("runtime_config")
		switch ctx.Request.Method {
		case http.MethodGet, http.MethodHead:
		default:
			ctx.AbortWithStatus(http.StatusMethodNotAllowed)
			return
		}

		ctx.Header("Cache-Control", "no-store")
		ctx.Header("Pragma", "no-cache")
		if ctx.Request.Method == http.MethodHead {
			ctx.Status(http.StatusOK)
			return
		}

		// Build tools configuration for frontend
		toolsConfig := gin.H{
			"web_search":       true,
			"web_fetch":        true,
			"ask_user":         true,
			"get_user_request": true,
			"extract_key_info": true,
			"file_io":          true,
		}
		if resolver != nil {
			toolsConfig["web_search"] = resolver.args.MCPToolsSettings.WebSearchEnabled
			toolsConfig["web_fetch"] = resolver.args.MCPToolsSettings.WebFetchEnabled
			toolsConfig["ask_user"] = resolver.args.MCPToolsSettings.AskUserEnabled
			toolsConfig["get_user_request"] = resolver.args.MCPToolsSettings.GetUserRequestEnabled
			toolsConfig["extract_key_info"] = resolver.args.MCPToolsSettings.ExtractKeyInfoEnabled
			toolsConfig["file_io"] = resolver.args.MCPToolsSettings.FileIOEnabled
		}

		siteConfig := siteConfigs.resolveForRequest(ctx.Request)
		if siteConfig.PublicBasePath == "" {
			siteConfig.PublicBasePath = prefix.public
		}
		logger.Debug("runtime config resolved",
			zap.String("host", requestHost(ctx.Request)),
			zap.String("site_id", siteConfig.ID),
		)

		ctx.JSON(http.StatusOK, gin.H{
			"urlPrefix":      prefix.internal,
			"publicBasePath": siteConfig.PublicBasePath,
			"site":           siteConfig,
			"tools":          toolsConfig,
		})
	}

	runtimeConfigPath := prefix.join("/runtime-config.json")
	server.Any(runtimeConfigPath, runtimeConfigHandler)
	if runtimeConfigPath != "/runtime-config.json" {
		server.Any("/runtime-config.json", runtimeConfigHandler)
	}

	if frontendSPA != nil {
		server.NoRoute(func(ctx *gin.Context) {
			if ctx.Request.Method != http.MethodGet && ctx.Request.Method != http.MethodHead {
				ctx.AbortWithStatus(http.StatusNotFound)
				return
			}

			requestPath := ctx.Request.URL.Path
			if !prefix.matches(requestPath) {
				if prefix.internal != "" && allowUnprefixedAsset(requestPath) {
					ctx.Request.URL.Path = prefix.join(requestPath)
					requestPath = ctx.Request.URL.Path
				} else {
					ctx.AbortWithStatus(http.StatusNotFound)
					return
				}
			}

			if strings.Contains(requestPath, ".") {
				ctx.Status(http.StatusOK)
				frontendSPA.ServeHTTP(ctx.Writer, ctx.Request)
				return
			}

			accept := ctx.Request.Header.Get("Accept")
			if accept != "" && !strings.Contains(accept, "text/html") && !strings.Contains(accept, "*/*") {
				ctx.AbortWithStatus(http.StatusNotFound)
				return
			}

			ctx.Status(http.StatusOK)
			frontendSPA.ServeHTTP(ctx.Writer, ctx.Request)
		})
	}

	log.Logger.Info("listening on http", zap.String("addr", addr))
	log.Logger.Panic("httpServer exit", zap.Error(server.Run(addr)))
}

func shouldServeFrontend(r *http.Request) bool {
	if r == nil {
		return false
	}

	switch r.Method {
	case http.MethodGet, http.MethodHead:
	default:
		return false
	}

	accept := r.Header.Get("Accept")
	if accept == "" {
		return true
	}
	accept = strings.ToLower(accept)
	if strings.Contains(accept, "text/html") || strings.Contains(accept, "application/xhtml+xml") || strings.Contains(accept, "*/*") {
		return true
	}

	return false
}

func allowCORS(ctx *gin.Context) {
	origin := ctx.Request.Header.Get("Origin")
	allowedOrigin := ""

	// Always add debug logging for CORS requests
	log.Logger.Debug("CORS request",
		zap.String("method", ctx.Request.Method),
		zap.String("origin", origin),
		zap.String("user-agent", ctx.Request.Header.Get("User-Agent")))

	if origin != "" {
		parsedOriginURL, err := url.Parse(origin)
		if err == nil {
			host := strings.ToLower(parsedOriginURL.Hostname())
			// Allow *.laisky.com, laisky.com, and localhost for development
			if strings.HasSuffix(host, ".laisky.com") ||
				host == "laisky.com" ||
				host == "localhost" ||
				strings.HasPrefix(host, "127.0.0.1") ||
				strings.HasPrefix(host, "192.168.") ||
				strings.HasPrefix(host, "10.") ||
				isCarrierGradeNatIP(host) {
				allowedOrigin = origin
				log.Logger.Debug("CORS: origin allowed",
					zap.String("origin", origin),
					zap.String("host", host))
			} else {
				// Add debug logging for denied origins
				log.Logger.Debug("CORS: origin denied",
					zap.String("origin", origin),
					zap.String("host", host))
			}
		} else {
			// Add debug logging for parsing errors
			log.Logger.Debug("CORS: failed to parse origin",
				zap.String("origin", origin),
				zap.Error(err))
		}
	}

	// Set CORS headers
	if allowedOrigin != "" {
		ctx.Header("Access-Control-Allow-Origin", allowedOrigin)
		ctx.Header("Access-Control-Allow-Headers", "*")
		ctx.Header("Access-Control-Allow-Credentials", "true")
		ctx.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS, HEAD, PATCH")
		ctx.Header("Access-Control-Max-Age", "86400") // 24 hours
		ctx.Header("Vary", "Origin")                  // Indicate that the response varies based on the Origin header

		if ctx.Request.Method == http.MethodOptions {
			log.Logger.Debug("CORS: handling preflight request", zap.String("origin", origin))
			ctx.AbortWithStatus(http.StatusNoContent)
			return
		}
	} else if origin != "" && ctx.Request.Method == http.MethodOptions {
		// If Origin is present, but not allowed, and it's an OPTIONS request (preflight)
		// Deny the preflight request from disallowed origins.
		log.Logger.Debug("CORS: denying preflight for disallowed origin",
			zap.String("origin", origin))
		ctx.AbortWithStatus(http.StatusForbidden)
		return
	} else if origin == "" && ctx.Request.Method == http.MethodOptions {
		// Handle OPTIONS requests without Origin header (some tools/browsers)
		log.Logger.Debug("CORS: OPTIONS request without Origin header")
		ctx.Header("Access-Control-Allow-Origin", "*")
		ctx.Header("Access-Control-Allow-Headers", "*")
		ctx.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS, HEAD")
		ctx.Header("Access-Control-Max-Age", "86400")
		ctx.AbortWithStatus(http.StatusNoContent)
		return
	}

	ctx.Next()
}

// newStatusHandler returns a handler that reports MCP service availability.
func newStatusHandler() gin.HandlerFunc {
	return func(ctx *gin.Context) {
		ctx.Header("Allow", "GET, HEAD, OPTIONS")

		switch ctx.Request.Method {
		case http.MethodGet:
			ctx.String(http.StatusOK, "ok")
		case http.MethodHead, http.MethodOptions:
			ctx.Status(http.StatusOK)
		default:
			ctx.AbortWithStatus(http.StatusMethodNotAllowed)
		}
	}
}

func isCarrierGradeNatIP(host string) bool {
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}

	ip = ip.To4()
	if ip == nil {
		return false
	}

	return ip[0] == 100 && ip[1] >= 64 && ip[1] <= 127
}

func allowUnprefixedAsset(path string) bool {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return false
	}

	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "/assets/") {
		return true
	}

	switch lower {
	case "/vite.svg", "/favicon.ico", "/robots.txt", "/manifest.json":
		return true
	default:
		return false
	}
}
