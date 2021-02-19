package main

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/Laisky/go-utils"
	laisky_blog_graphql "github.com/Laisky/laisky-blog-graphql"
	"github.com/Laisky/laisky-blog-graphql/libs"
	"github.com/Laisky/zap"
	"github.com/spf13/pflag"
)

func setupSettings(ctx context.Context) {
	var err error
	// mode
	if utils.Settings.GetBool("debug") {
		fmt.Println("run in debug mode")
		utils.Settings.Set("log-level", "debug")
	} else { // prod mode
		fmt.Println("run in prod mode")
	}

	// clock
	utils.SetupClock(100 * time.Millisecond)

	// load configuration
	cfgPath := utils.Settings.GetString("config")
	utils.Settings.Set("cfg_dir", filepath.Dir(cfgPath))
	if err = utils.Settings.LoadFromFile(cfgPath); err != nil {
		libs.Logger.Panic("load configuration",
			zap.Error(err),
			zap.String("config", cfgPath))
	} else {
		libs.Logger.Info("load configuration",
			zap.String("config", cfgPath))
	}
}

func setupLogger(ctx context.Context) {
	// log
	// alertPusher, err := utils.NewAlertPusherWithAlertType(
	// 	ctx,
	// 	utils.Settings.GetString("settings.logger.push_api"),
	// 	utils.Settings.GetString("settings.logger.alert_type"),
	// 	utils.Settings.GetString("settings.logger.push_token"),
	// )
	// if err != nil {
	// 	libs.Logger.Panic("create AlertPusher", zap.Error(err))
	// }
	//
	// libs.Logger = libs.Logger.WithOptions(
	// 	zap.HooksWithFields(alertPusher.GetZapHook()),
	// ).Named("laisky-graphql")

	libs.Logger = libs.Logger.Named("laisky-graphql")

	lvl := utils.Settings.GetString("log-level")
	if err := libs.Logger.ChangeLevel(lvl); err != nil {
		libs.Logger.Panic("change log level", zap.Error(err), zap.String("level", lvl))
	}
}

func setupArgs() {
	pflag.Bool("debug", false, "run in debug mode")
	pflag.Bool("dry", false, "run in dry mode")
	pflag.String("addr", "localhost:8080", "like `localhost:8080`")
	pflag.String("dbaddr", "localhost:8080", "like `localhost:8080`")
	pflag.StringP("config", "c", "/etc/laisky-blog-graphql/settings.yml", "config file path")
	pflag.String("log-level", "info", "`debug/info/error`")
	pflag.StringSliceP("tasks", "t", []string{}, "which tasks want to runnning, like\n ./main -t t1,t2,heartbeat")
	pflag.Int("heartbeat", 60, "heartbeat seconds")
	pflag.Parse()
	if err := utils.Settings.BindPFlags(pflag.CommandLine); err != nil {
		libs.Logger.Panic("parse command args", zap.Error(err))
	}
}

func main() {
	ctx := context.Background()

	setupArgs()
	setupSettings(ctx)
	setupLogger(ctx)

	defer func() { _ = utils.Logger.Sync() }()
	if err := laisky_blog_graphql.SetupJWT([]byte(utils.Settings.GetString("settings.secret"))); err != nil {
		libs.Logger.Panic("setup jwt", zap.Error(err))
	}

	c := laisky_blog_graphql.NewControllor()
	c.Run(ctx)
}
