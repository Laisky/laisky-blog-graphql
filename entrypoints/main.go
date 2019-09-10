package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"

	"github.com/Laisky/go-utils"
	laisky_blog_graphql "github.com/Laisky/laisky-blog-graphql"
	"github.com/Laisky/zap"
)

func setupSettings() {
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

	// load configuration
	cfgDirPath := utils.Settings.GetString("config")
	if err := utils.Settings.Setup(cfgDirPath); err != nil {
		utils.Logger.Panic("can not load config from disk",
			zap.String("dirpath", cfgDirPath))
	} else {
		utils.Logger.Info("success load configuration from dir",
			zap.String("dirpath", cfgDirPath))
	}
}

func setupArgs() {
	pflag.Bool("debug", false, "run in debug mode")
	pflag.Bool("dry", false, "run in dry mode")
	pflag.String("addr", "localhost:8080", "like `localhost:8080`")
	pflag.String("dbaddr", "localhost:8080", "like `localhost:8080`")
	pflag.String("config", "/etc/laisky-blog-graphql/settings", "config file directory path")
	pflag.String("log-level", "info", "`debug/info/error`")
	pflag.Int("heartbeat", 60, "heartbeat seconds")
	pflag.Parse()
	viper.BindPFlags(pflag.CommandLine)
}

func main() {
	defer utils.Logger.Sync()
	setupArgs()
	setupSettings()

	laisky_blog_graphql.DialDB(context.Background())
	laisky_blog_graphql.RunServer(utils.Settings.GetString("addr"))
}
