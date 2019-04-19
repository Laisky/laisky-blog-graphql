package laisky_blog_graphql

import (
	utils "github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
	"github.com/kataras/iris"
	"github.com/kataras/iris/middleware/pprof"
)

var (
	Server = iris.New()
)

func RunServer(addr string) {
	Server.Any("/health", func(ctx iris.Context) {
		ctx.Write([]byte("Hello, World"))
	})

	// supported action:
	// cmdline, profile, symbol, goroutine, heap, threadcreate, debug/block
	Server.Any("/admin/pprof/{action:path}", pprof.New())

	utils.Logger.Info("listening on http", zap.String("addr", addr))
	utils.Logger.Panic("httpserver exit", zap.Error(Server.Run(iris.Addr(addr), iris.WithConfiguration(iris.Configuration{
		DisablePathCorrection:            false,
		DisablePathCorrectionRedirection: true,
	}))))
}
