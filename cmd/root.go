package cmd

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"laisky-blog-graphql/internal/web"
	"laisky-blog-graphql/library/log"

	gutils "github.com/Laisky/go-utils"
	gcmd "github.com/Laisky/go-utils/cmd"
	"github.com/Laisky/zap"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "laisky-blog-graphql",
	Short: "laisky-blog-graphql",
	Long:  `graphql API service for laisky`,
	Args:  gcmd.NoExtraArgs,
	PreRunE: func(cmd *cobra.Command, args []string) error {
		return gutils.Settings.BindPFlags(cmd.Flags())
	},
	Run: func(_ *cobra.Command, args []string) {
		ctx := context.Background()

		setupSettings(ctx)
		setupLogger(ctx)

		defer func() { _ = gutils.Logger.Sync() }()
		if err := web.SetupJWT([]byte(gutils.Settings.GetString("settings.secret"))); err != nil {
			log.Logger.Panic("setup jwt", zap.Error(err))
		}

		c := web.NewControllor()
		c.Run(ctx)
	},
}

func setupSettings(ctx context.Context) {
	var err error
	// mode
	if gutils.Settings.GetBool("debug") {
		fmt.Println("run in debug mode")
		gutils.Settings.Set("log-level", "debug")
	} else { // prod mode
		fmt.Println("run in prod mode")
	}

	// clock
	gutils.SetupClock(100 * time.Millisecond)

	// load configuration
	cfgPath := gutils.Settings.GetString("config")
	gutils.Settings.Set("cfg_dir", filepath.Dir(cfgPath))
	if err = gutils.Settings.LoadFromFile(cfgPath); err != nil {
		log.Logger.Panic("load configuration",
			zap.Error(err),
			zap.String("config", cfgPath))
	} else {
		log.Logger.Info("load configuration",
			zap.String("config", cfgPath))
	}
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
	rootCmd.Flags().Bool("debug", false, "run in debug mode")
	rootCmd.Flags().Bool("dry", false, "run in dry mode")
	rootCmd.Flags().String("addr", "localhost:8080", "like `localhost:8080`")
	rootCmd.Flags().String("dbaddr", "localhost:8080", "like `localhost:8080`")
	rootCmd.Flags().StringP("config", "c", "/etc/laisky-blog-graphql/settings.yml", "config file path")
	rootCmd.Flags().String("log-level", "info", "`debug/info/error`")
	rootCmd.Flags().StringSliceP("tasks", "t", []string{}, "which tasks want to runnning, like\n ./main -t t1,t2,heartbeat")
	rootCmd.Flags().Int("heartbeat", 60, "heartbeat seconds")
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		gutils.Logger.Panic("start", zap.Error(err))
	}
}
