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
	ginMw "github.com/Laisky/gin-middlewares/v6"
	gconfig "github.com/Laisky/go-config/v2"
	"github.com/Laisky/zap"
	"github.com/gin-gonic/gin"
	"github.com/vektah/gqlparser/v2/gqlerror"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/askuser"
	"github.com/Laisky/laisky-blog-graphql/library/log"
)

var (
	server = gin.New()
)

func RunServer(addr string, resolver *Resolver) {
	server.Use(
		gin.Recovery(),
		ginMw.NewLoggerMiddleware(
			ginMw.WithLoggerMwColored(),
			ginMw.WithLevel(log.Logger.Level().String()),
			ginMw.WithLogger(log.Logger.Named("gin")),
		),
		allowCORS,
	)
	if !gconfig.Shared.GetBool("debug") {
		gin.SetMode(gin.ReleaseMode)
	}

	if err := ginMw.EnableMetric(server); err != nil {
		log.Logger.Panic("enable metric server", zap.Error(err))
	}

	if resolver != nil && (resolver.args.BingSearchEngine != nil || resolver.args.AskUserService != nil) {
		mcpServer, err := mcp.NewServer(resolver.args.BingSearchEngine, resolver.args.AskUserService, log.Logger)
		if err != nil {
			log.Logger.Error("init mcp server", zap.Error(err))
		} else {
			server.Any("/mcp", gin.WrapH(mcpServer.Handler()))
			if resolver.args.AskUserService != nil {
				askUserMux := askuser.NewHTTPHandler(resolver.args.AskUserService, log.Logger.Named("ask_user_http"))
				server.Any("/mcp/tools/ask_user", gin.WrapH(askUserMux))
				server.Any("/mcp/tools/ask_user/*path", gin.WrapH(http.StripPrefix("/mcp/tools/ask_user", askUserMux)))
			}
		}
	} else {
		bingNil := resolver != nil && resolver.args.BingSearchEngine == nil
		askNil := resolver != nil && resolver.args.AskUserService == nil
		log.Logger.Warn("skip mcp server initialization",
			zap.Bool("resolver_nil", resolver == nil),
			zap.Bool("bing_search_engine_nil", bingNil),
			zap.Bool("ask_user_service_nil", askNil),
		)
	}

	server.Any("/health", func(ctx *gin.Context) {
		ctx.String(http.StatusOK, "hello, world")
	})

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
	server.Any("/ui/", ginMw.FromStd(playground.Handler("GraphQL playground", "/query/")))
	server.Any("/query/", ginMw.FromStd(h.ServeHTTP))
	server.Any("/query/v2/", ginMw.FromStd(h.ServeHTTP))

	if spa := newFrontendSPAHandler(log.Logger.Named("frontend_spa")); spa != nil {
		server.NoRoute(func(ctx *gin.Context) {
			if ctx.Request.Method != http.MethodGet && ctx.Request.Method != http.MethodHead {
				ctx.AbortWithStatus(http.StatusNotFound)
				return
			}

			if strings.Contains(ctx.Request.URL.Path, ".") {
				spa.ServeHTTP(ctx.Writer, ctx.Request)
				return
			}

			accept := ctx.Request.Header.Get("Accept")
			if accept != "" && !strings.Contains(accept, "text/html") && !strings.Contains(accept, "*/*") {
				ctx.AbortWithStatus(http.StatusNotFound)
				return
			}

			spa.ServeHTTP(ctx.Writer, ctx.Request)
		})
	}

	log.Logger.Info("listening on http", zap.String("addr", addr))
	log.Logger.Panic("httpServer exit", zap.Error(server.Run(addr)))
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
		ctx.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS, HEAD")
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
