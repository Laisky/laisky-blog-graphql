// Package web gin server
package web

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/99designs/gqlgen/graphql"
	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/extension"
	"github.com/99designs/gqlgen/graphql/handler/transport"
	"github.com/99designs/gqlgen/graphql/playground"
	ginMw "github.com/Laisky/gin-middlewares/v5"
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
			ginMw.WithLogger(log.Logger.Named("gin")),
		),
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
		if !strings.Contains(e.Error(), "token invalidate for ") &&
			!strings.Contains(e.Error(), "ValidateTokenForAlertType") {
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
