package cmd

import (
	"context"
	"os"

	"github.com/Laisky/errors/v2"

	"github.com/Laisky/laisky-blog-graphql/internal/web"
	blogCtl "github.com/Laisky/laisky-blog-graphql/internal/web/blog/controller"
	blogDao "github.com/Laisky/laisky-blog-graphql/internal/web/blog/dao"
	blogModel "github.com/Laisky/laisky-blog-graphql/internal/web/blog/model"
	blogSvc "github.com/Laisky/laisky-blog-graphql/internal/web/blog/service"
	telegramCtl "github.com/Laisky/laisky-blog-graphql/internal/web/telegram/controller"
	telegramDao "github.com/Laisky/laisky-blog-graphql/internal/web/telegram/dao"
	telegramModel "github.com/Laisky/laisky-blog-graphql/internal/web/telegram/model"
	telegramSvc "github.com/Laisky/laisky-blog-graphql/internal/web/telegram/service"
	"github.com/Laisky/laisky-blog-graphql/library/db/arweave"
	"github.com/Laisky/laisky-blog-graphql/library/log"

	gconfig "github.com/Laisky/go-config/v2"
	gcmd "github.com/Laisky/go-utils/v4/cmd"
	"github.com/Laisky/zap"
	"github.com/spf13/cobra"
)

var apiCMD = &cobra.Command{
	Use:   "api",
	Short: "api",
	Long:  `graphql API service for laisky`,
	Args:  gcmd.NoExtraArgs,
	PreRun: func(cmd *cobra.Command, args []string) {
		ctx := context.Background()
		if err := initialize(ctx, cmd); err != nil {
			log.Logger.Panic("init", zap.Error(err))
		}
	},
	Run: func(cmd *cobra.Command, args []string) {
		if err := runAPI(); err != nil {
			log.Logger.Panic("run api", zap.Error(err))
		}
	},
}

func init() {
	rootCMD.AddCommand(apiCMD)
}

func runAPI() error {
	ctx := context.Background()
	logger := log.Logger.Named("api")

	var args web.ResolverArgs

	{ // setup telegram
		monitorDB, err := telegramModel.NewMonitorDB(ctx)
		if err != nil {
			return errors.Wrap(err, "new monitor db")
		}
		telegramDB, err := telegramModel.NewTelegramDB(ctx)
		if err != nil {
			return errors.Wrap(err, "new telegram db")
		}

		var botToken = gconfig.Shared.GetString("settings.telegram.token")
		if os.Getenv("TELEGRAM_BOT_TOKEN") != "" {
			logger.Info("rewrite telegram bot token from env")
			botToken = os.Getenv("TELEGRAM_BOT_TOKEN")
		}

		args.TelegramSvc, err = telegramSvc.New(ctx,
			telegramDao.NewMonitor(monitorDB),
			telegramDao.NewTelegram(telegramDB),
			botToken,
			gconfig.Shared.GetString("settings.telegram.api"),
		)
		if err != nil {
			logger.Error("new telegram service", zap.Error(err))
			// return errors.Wrap(err, "new telegram service")
		} else {
			args.TelegramCtl = telegramCtl.NewTelegram(ctx, args.TelegramSvc)
		}
	}

	// arweave := arweave.NewAkrod(
	// 	gconfig.Shared.GetStringSlice("settings.db.akord.apikeys"),
	// )
	arweave := arweave.NewArdrive(
		gconfig.S.GetString("settings.arweave.wallet_file"),
		gconfig.S.GetString("settings.arweave.folder_id"),
	)

	{ // setup blog
		blogDB, err := blogModel.NewDB(ctx)
		if err != nil {
			return errors.Wrap(err, "new blog db")
		}
		blogDao := blogDao.New(logger.Named("blog_dao"), blogDB, arweave)
		args.BlogSvc, err = blogSvc.New(ctx, logger.Named("blog_svc"), blogDao)
		if err != nil {
			return errors.Wrap(err, "new blog service")
		}

		args.BlogCtl = blogCtl.New(args.BlogSvc)
	}

	resolver := web.NewResolver(args)
	web.RunServer(gconfig.Shared.GetString("listen"), resolver)
	return nil
}
