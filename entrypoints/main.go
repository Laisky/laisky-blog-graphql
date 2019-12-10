package main

import (
	"context"
	"fmt"
	"time"

	laisky_blog_graphql "github.com/Laisky/laisky-blog-graphql"

	"github.com/spf13/pflag"

	"github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
)

func setupSettings(ctx context.Context) {
	var err error
	// mode
	if utils.Settings.GetBool("debug") {
		fmt.Println("run in debug mode")
		utils.Settings.Set("log-level", "debug")
		_ = utils.Logger.ChangeLevel("debug")
	} else { // prod mode
		fmt.Println("run in prod mode")
	}

	// clock
	utils.SetupClock(100 * time.Millisecond)

	// load configuration
	cfgDirPath := utils.Settings.GetString("config")
	if err = utils.Settings.Setup(cfgDirPath); err != nil {
		utils.Logger.Panic("can not load config from disk",
			zap.String("dirpath", cfgDirPath))
	} else {
		utils.Logger.Info("success load configuration from dir",
			zap.String("dirpath", cfgDirPath))
	}
}

func setupLogger(ctx context.Context) {
	// log
	alertPusher, err := utils.NewAlertPusherWithAlertType(
		ctx,
		utils.Settings.GetString("settings.logger.push_api"),
		utils.Settings.GetString("settings.logger.alert_type"),
		utils.Settings.GetString("settings.logger.push_token"),
	)
	if err != nil {
		utils.Logger.Panic("create AlertPusher", zap.Error(err))
	}
	hook := utils.NewAlertHook(alertPusher)
	if _, err := utils.SetDefaultLogger(
		"laisky-blog-graphql",
		utils.Settings.GetString("log-level"),
		zap.HooksWithFields(hook.GetZapHook())); err != nil {
		utils.Logger.Panic("setup logger", zap.Error(err))
	}
}

func setupArgs() {
	pflag.Bool("debug", false, "run in debug mode")
	pflag.Bool("dry", false, "run in dry mode")
	pflag.String("addr", "localhost:8080", "like `localhost:8080`")
	pflag.String("dbaddr", "localhost:8080", "like `localhost:8080`")
	pflag.String("config", "/etc/laisky-blog-graphql/settings", "config file directory path")
	pflag.String("log-level", "info", "`debug/info/error`")
	pflag.StringSliceP("tasks", "t", []string{}, "which tasks want to runnning, like\n ./main -t t1,t2,heartbeat")
	pflag.Int("heartbeat", 60, "heartbeat seconds")
	pflag.Parse()
	if err := utils.Settings.BindPFlags(pflag.CommandLine); err != nil {
		utils.Logger.Panic("parse command args", zap.Error(err))
	}
}

func main() {
	ctx := context.Background()

	setupArgs()
	setupSettings(ctx)
	setupLogger(ctx)

	laisky_blog_graphql.SetupJWT([]byte(utils.Settings.GetString("settings.secret")))

	c := laisky_blog_graphql.NewControllor()
	c.Run(ctx)
}
