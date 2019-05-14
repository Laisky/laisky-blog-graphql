package laisky_blog_graphql

import (
	utils "github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
	prometheusMiddleware "github.com/iris-contrib/middleware/prometheus"
	"github.com/kataras/iris"
	"github.com/kataras/iris/middleware/pprof"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	Server = iris.New()
)

func RunServer(addr string) {
	Server.UseGlobal(LoggerMiddleware)

	Server.Any("/health", func(ctx iris.Context) {
		ctx.Write([]byte("Hello, World"))
	})

	m := prometheusMiddleware.New("serviceName", 0.3, 1.2, 5.0)
	Server.Use(m.ServeHTTP)
	// supported action:
	// cmdline, profile, symbol, goroutine, heap, threadcreate, debug/block
	Server.Any("/pprof/{action:path}", pprof.New())
	Server.Get("/metrics", iris.FromStd(promhttp.Handler()))

	utils.Logger.Info("listening on http", zap.String("addr", addr))
	utils.Logger.Panic("httpserver exit", zap.Error(Server.Run(iris.Addr(addr), iris.WithConfiguration(iris.Configuration{
		DisablePathCorrection:            false,
		DisablePathCorrectionRedirection: true,
	}))))
}

func LoggerMiddleware(ctx iris.Context) {
	utils.Logger.Debug("request",
		zap.String("path", ctx.Path()),
		zap.String("method", ctx.Method()),
	)
	ctx.Next()
}
