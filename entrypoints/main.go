package main

import (
	"fmt"
	"time"

	"github.com/99designs/gqlgen/handler"
	"github.com/Laisky/go-utils"
	"github.com/Laisky/laisky-blog-graphql"
	"github.com/kataras/iris"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

func SetupSettings() {
	// mode
	if utils.Settings.GetBool("debug") {
		fmt.Println("run in debug mode")
		utils.Settings.Set("log-level", "debug")
	} else { // prod mode
		fmt.Println("run in prod mode")
	}

	// log
	utils.SetupLogger(utils.Settings.GetString("log-level"))

	// clock
	utils.SetupClock(100 * time.Millisecond)
}

func SetupArgs() {
	pflag.Bool("debug", false, "run in debug mode")
	pflag.Bool("dry", false, "run in dry mode")
	pflag.String("addr", "localhost:8080", "like `localhost:8080`")
	pflag.String("dbaddr", "localhost:8080", "like `localhost:8080`")
	pflag.String("log-level", "info", "`debug/info/error`")
	pflag.Int("heartbeat", 60, "heartbeat seconds")
	pflag.Parse()
	viper.BindPFlags(pflag.CommandLine)
}

func main() {
	defer utils.Logger.Sync()
	SetupArgs()
	SetupSettings()

	laisky_blog_graphql.DialDB(utils.Settings.GetString("dbaddr"))

	laisky_blog_graphql.Server.Handle("ANY", "/ui/", iris.FromStd(handler.Playground("GraphQL playground", "/graphql/query/")))
	laisky_blog_graphql.Server.Handle("ANY", "/query/", iris.FromStd(handler.GraphQL(laisky_blog_graphql.NewExecutableSchema(laisky_blog_graphql.Config{Resolvers: &laisky_blog_graphql.Resolver{}}))))
	laisky_blog_graphql.RunServer(utils.Settings.GetString("addr"))
}
