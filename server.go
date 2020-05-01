package laisky_blog_graphql

import (
	"net/http"

	"github.com/99designs/gqlgen/handler"
	ginMiddlewares "github.com/Laisky/gin-middlewares"
	utils "github.com/Laisky/go-utils"
	"github.com/Laisky/laisky-blog-graphql/log"
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
		log.GetLog().Panic("try to setup auth got error", zap.Error(err))
	}
	server.Use(LoggerMiddleware)
	ginMiddlewares.EnableMetric(server)

	server.Any("/health", func(ctx *gin.Context) {
		ctx.String(http.StatusOK, "hello, world")
	})

	server.Any("/ui/", ginMiddlewares.FromStd(handler.Playground("GraphQL playground", "/graphql/query/")))
	server.Any("/query/", ginMiddlewares.FromStd(handler.GraphQL(NewExecutableSchema(Config{Resolvers: &Resolver{}}))))

	log.GetLog().Info("listening on http", zap.String("addr", addr))
	log.GetLog().Panic("httpServer exit", zap.Error(server.Run(addr)))
}

func LoggerMiddleware(ctx *gin.Context) {
	log.GetLog().Debug("request",
		zap.String("path", ctx.Request.RequestURI),
		zap.String("method", ctx.Request.Method),
	)
	ctx.Next()
}
