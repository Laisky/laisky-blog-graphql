// Package web gin server
package web

import (
	"context"
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

	log.Logger.Info("listening on http", zap.String("addr", addr))
	log.Logger.Panic("httpServer exit", zap.Error(server.Run(addr)))
}

func allowCORS(ctx *gin.Context) {
	origin := ctx.Request.Header.Get("Origin")
	allowedOrigin := ""

	if origin != "" {
		parsedOriginURL, err := url.Parse(origin)
		if err == nil {
			host := strings.ToLower(parsedOriginURL.Hostname())
			// Allow *.laisky.com and laisky.com
			if strings.HasSuffix(host, ".laisky.com") || host == "laisky.com" {
				allowedOrigin = origin
			}
		}
	}

	if allowedOrigin != "" {
		ctx.Header("Access-Control-Allow-Origin", allowedOrigin)
		ctx.Header("Access-Control-Allow-Credentials", "true")
		ctx.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS, HEAD")
		ctx.Header("Access-Control-Allow-Headers", "Content-Type, Authorization, Accept, Origin, X-CSRF-Token, X-Requested-With, sentry-trace, baggage")
		ctx.Header("Access-Control-Max-Age", "86400") // 24 hours
		ctx.Header("Vary", "Origin")                  // Indicate that the response varies based on the Origin header

		if ctx.Request.Method == http.MethodOptions {
			ctx.AbortWithStatus(http.StatusNoContent)
			return
		}
	} else if origin != "" && ctx.Request.Method == http.MethodOptions {
		// If Origin is present, but not allowed, and it's an OPTIONS request (preflight)
		// Deny the preflight request from disallowed origins.
		ctx.AbortWithStatus(http.StatusForbidden)
		return
	}

	ctx.Next()
}
