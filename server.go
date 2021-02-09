package laisky_blog_graphql

import (
	"net/http"
	"time"

	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/extension"
	"github.com/99designs/gqlgen/graphql/handler/transport"
	"github.com/99designs/gqlgen/graphql/playground"
	ginMiddlewares "github.com/Laisky/gin-middlewares"
	utils "github.com/Laisky/go-utils"
	"github.com/Laisky/laisky-blog-graphql/libs"
	"github.com/Laisky/zap"
	"github.com/gin-gonic/gin"
)

var (
	server = gin.New()
	auth   *ginMiddlewares.Auth
)

func setupAuth() (err error) {
	auth, err = ginMiddlewares.NewAuth([]byte(utils.Settings.GetString("settings.secret")))
	return
}

func RunServer(addr string) {
	server.Use(gin.Recovery())
	if !utils.Settings.GetBool("debug") {
		gin.SetMode(gin.ReleaseMode)
	}

	if err := setupAuth(); err != nil {
		libs.Logger.Panic("try to setup auth got error", zap.Error(err))
	}

	server.Use(ginMiddlewares.GetLoggerMiddleware(libs.Logger.Named("gin")))
	if err := ginMiddlewares.EnableMetric(server); err != nil {
		libs.Logger.Panic("enable metric server", zap.Error(err))
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
	// server.Any("/ui/", ginMiddlewares.FromStd(playground.Handler("GraphQL playground", "/query/")))
	h.Use(extension.Introspection{})
	server.Any("/ui/", ginMiddlewares.FromStd(playground.Handler("GraphQL playground", "/graphql/query/")))
	server.Any("/query/", ginMiddlewares.FromStd(h.ServeHTTP))

	libs.Logger.Info("listening on http", zap.String("addr", addr))
	libs.Logger.Panic("httpServer exit", zap.Error(server.Run(addr)))
}
