package laisky_blog_graphql

import (
	irisMiddlewares "github.com/Laisky/go-utils/iris-middlewares"

	"github.com/99designs/gqlgen/handler"
	prometheusMiddleware "github.com/iris-contrib/middleware/prometheus"
	"github.com/kataras/iris"
	"github.com/kataras/iris/middleware/pprof"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	utils "github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
)

var (
	server = iris.New()
	auth   *irisMiddlewares.Auth
)

func setupAuth() {
	cfg := irisMiddlewares.AuthCfg
	cfg.Secret = utils.Settings.GetString("settings.secret")
	auth = irisMiddlewares.NewAuth(cfg)
}

func RunServer(addr string) {
	setupAuth()
	server.UseGlobal(LoggerMiddleware)

	server.Any("/health", func(ctx iris.Context) {
		ctx.Write([]byte("Hello, World"))
	})

	m := prometheusMiddleware.New("serviceName", 0.3, 1.2, 5.0)
	server.Use(m.ServeHTTP)
	// supported action:
	// cmdline, profile, symbol, goroutine, heap, threadcreate, debug/block
	server.Any("/pprof/{action:path}", pprof.New())
	server.Get("/metrics", iris.FromStd(promhttp.Handler()))

	server.Handle("ANY", "/ui/", irisMiddlewares.FromStd(handler.Playground("GraphQL playground", "/graphql/query/")))
	server.Handle("ANY", "/query/", irisMiddlewares.FromStd(handler.GraphQL(NewExecutableSchema(Config{Resolvers: &Resolver{}}))))

	utils.Logger.Info("listening on http", zap.String("addr", addr))
	utils.Logger.Panic("httpServer exit", zap.Error(server.Run(iris.Addr(addr), iris.WithConfiguration(iris.Configuration{
		DisablePathCorrection:            false,
		DisablePathCorrectionRedirection: true,
	}))))
}

const IrisCtxKey = "irisctx"

func LoggerMiddleware(ctx iris.Context) {
	utils.Logger.Debug("request",
		zap.String("path", ctx.Path()),
		zap.String("method", ctx.Method()),
	)
	ctx.Next()
}
