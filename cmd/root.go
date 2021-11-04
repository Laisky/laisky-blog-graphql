package cmd

import (
	"context"
	"fmt"
	"time"

	blog "laisky-blog-graphql/internal/web/blog/controller"
	telegram "laisky-blog-graphql/internal/web/telegram/controller"
	twitter "laisky-blog-graphql/internal/web/twitter/controller"
	"laisky-blog-graphql/library/auth"
	"laisky-blog-graphql/library/config"
	"laisky-blog-graphql/library/jwt"
	"laisky-blog-graphql/library/log"

	gutils "github.com/Laisky/go-utils"
	gcmd "github.com/Laisky/go-utils/cmd"
	"github.com/Laisky/zap"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

var rootCMD = &cobra.Command{
	Use:   "laisky-blog-graphql",
	Short: "laisky-blog-graphql",
	Long:  `graphql API service for laisky`,
	Args:  gcmd.NoExtraArgs,
}

func initialize(ctx context.Context, cmd *cobra.Command) error {
	if err := gutils.Settings.BindPFlags(cmd.Flags()); err != nil {
		return errors.Wrap(err, "bind pflags")
	}

	setupSettings(ctx)
	setupLogger(ctx)
	setupLibrary(ctx)
	setupModules(ctx)

	return nil
}

func setupModules(ctx context.Context) {
	blog.Initialize(ctx)
	twitter.Initialize(ctx)
	telegram.Initialize(ctx)
}

func setupLibrary(ctx context.Context) {
	if err := auth.Initialize([]byte(gutils.Settings.GetString("settings.secret"))); err != nil {
		log.Logger.Panic("init jwt", zap.Error(err))
	}

	if err := jwt.Initialize([]byte(gutils.Settings.GetString("settings.secret"))); err != nil {
		log.Logger.Panic("setup jwt", zap.Error(err))
	}

}

func setupSettings(ctx context.Context) {
	// mode
	if gutils.Settings.GetBool("debug") {
		fmt.Println("run in debug mode")
		gutils.Settings.Set("log-level", "debug")
	} else { // prod mode
		fmt.Println("run in prod mode")
	}

	// clock
	gutils.SetInternalClock(100 * time.Millisecond)

	// load configuration
	cfgPath := gutils.Settings.GetString("config")
	config.LoadFromFile(cfgPath)
}

func setupLogger(ctx context.Context) {
	// log
	// alertPusher, err := gutils.NewAlertPusherWithAlertType(
	// 	ctx,
	// 	gutils.Settings.GetString("settings.logger.push_api"),
	// 	gutils.Settings.GetString("settings.logger.alert_type"),
	// 	gutils.Settings.GetString("settings.logger.push_token"),
	// )
	// if err != nil {
	// 	log.Logger.Panic("create AlertPusher", zap.Error(err))
	// }
	//
	// library.Logger = log.Logger.WithOptions(
	// 	zap.HooksWithFields(alertPusher.GetZapHook()),
	// ).Named("laisky-graphql")

	lvl := gutils.Settings.GetString("log-level")
	if err := log.Logger.ChangeLevel(lvl); err != nil {
		log.Logger.Panic("change log level", zap.Error(err), zap.String("level", lvl))
	}
}

func init() {
	rootCMD.PersistentFlags().Bool("debug", false, "run in debug mode")
	rootCMD.PersistentFlags().Bool("dry", false, "run in dry mode")
	rootCMD.PersistentFlags().String("listen", "localhost:8080", "like `localhost:8080`")
	rootCMD.PersistentFlags().StringP("config", "c", "/etc/laisky-blog-graphql/settings.yml", "config file path")
	rootCMD.PersistentFlags().String("log-level", "info", "`debug/info/error`")
	rootCMD.PersistentFlags().StringSliceP("tasks", "t", []string{},
		"which tasks want to runnning, like\n ./main -t t1,t2,heartbeat")
	rootCMD.PersistentFlags().Int("heartbeat", 60, "heartbeat seconds")
}

func Execute() {
	if err := rootCMD.Execute(); err != nil {
		gutils.Logger.Panic("start", zap.Error(err))
	}
}
