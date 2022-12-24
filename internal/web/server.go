package web

import (
	"context"
	"net/http"
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

func RunServer(addr string) {
	server.Use(gin.Recovery())
	if !gconfig.Shared.GetBool("debug") {
		gin.SetMode(gin.ReleaseMode)
	}

	server.Use(ginMw.NewLoggerMiddleware(
		ginMw.WithLogger(log.Logger.Named("gin")),
	))
	if err := ginMw.EnableMetric(server); err != nil {
		log.Logger.Panic("enable metric server", zap.Error(err))
	}

	server.Any("/health", func(ctx *gin.Context) {
		ctx.String(http.StatusOK, "hello, world")
	})

	h := handler.New(NewExecutableSchema(Config{Resolvers: &Resolver{}}))
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
		log.Logger.Error("graphql server", zap.Error(e))
		return err
	})
	server.Any("/ui/", ginMw.FromStd(playground.Handler("GraphQL playground", "/query/")))
	server.Any("/query/", ginMw.FromStd(h.ServeHTTP))
	server.Any("/query/v2/", ginMw.FromStd(h.ServeHTTP))

	log.Logger.Info("listening on http", zap.String("addr", addr))
	log.Logger.Panic("httpServer exit", zap.Error(server.Run(addr)))
}
