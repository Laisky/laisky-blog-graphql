package main

import (
	"context"
	"fmt"
	"time"

	"github.com/Laisky/go-utils"
	laisky_blog_graphql "github.com/Laisky/laisky-blog-graphql"
	"github.com/Laisky/laisky-blog-graphql/log"
	"github.com/Laisky/zap"
	"github.com/spf13/pflag"
)

func setupSettings(ctx context.Context) {
	var err error
	// mode
	if utils.Settings.GetBool("debug") {
		fmt.Println("run in debug mode")
		utils.Settings.Set("log-level", "debug")
		if err := log.GetLog().ChangeLevel("debug"); err != nil {
			log.GetLog().Panic("change log level to debug")
		}
	} else { // prod mode
		fmt.Println("run in prod mode")
	}

	// clock
	utils.SetupClock(100 * time.Millisecond)

	// load configuration
	cfgDirPath := utils.Settings.GetString("config")
	if err = utils.Settings.Setup(cfgDirPath); err != nil {
		log.GetLog().Panic("can not load config from disk",
			zap.String("dirpath", cfgDirPath))
	} else {
		log.GetLog().Info("success load configuration from dir",
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
		log.GetLog().Panic("create AlertPusher", zap.Error(err))
	}

	logger := log.GetLog().WithOptions(
		zap.HooksWithFields(alertPusher.GetZapHook()),
	).Named("laisky-graphql")
	log.SetLog(logger)

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
		log.GetLog().Panic("parse command args", zap.Error(err))
	}
}

func main() {
	ctx := context.Background()

	setupArgs()
	setupSettings(ctx)
	setupLogger(ctx)

	if err := laisky_blog_graphql.SetupJWT([]byte(utils.Settings.GetString("settings.secret"))); err != nil {
		log.GetLog().Panic("setup jwt", zap.Error(err))
	}

	c := laisky_blog_graphql.NewControllor()
	c.Run(ctx)
}
