package cmd

import (
	"context"
	"os"

	"github.com/Laisky/errors/v2"
	gconfig "github.com/Laisky/go-config/v2"
	gcmd "github.com/Laisky/go-utils/v5/cmd"
	"github.com/Laisky/zap"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/redis/go-redis/v9"
	"github.com/spf13/cobra"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/askuser"
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
	"github.com/Laisky/laisky-blog-graphql/library/db/postgres"
	rlibs "github.com/Laisky/laisky-blog-graphql/library/db/redis"
	"github.com/Laisky/laisky-blog-graphql/library/log"
	"github.com/Laisky/laisky-blog-graphql/library/search/google"
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

	arweave := arweave.NewArdrive(
		gconfig.S.GetString("settings.arweave.wallet_file"),
		gconfig.S.GetString("settings.arweave.folder_id"),
	)

	minioCli, err := minio.New(
		gconfig.S.GetString("settings.arweave.s3.endpoint"),
		&minio.Options{
			Creds: credentials.NewStaticV4(
				gconfig.S.GetString("settings.arweave.s3.access_key"),
				gconfig.S.GetString("settings.arweave.s3.secret"),
				"",
			),
			Secure: true,
		},
	)
	if err != nil {
		return errors.Wrap(err, "new minio client")
	}

	var args web.ResolverArgs

	// setup redis
	args.Rdb = rlibs.NewDB(&redis.Options{
		Addr: gconfig.S.GetString("settings.db.redis.addr"),
		DB:   gconfig.S.GetInt("settings.db.redis.db"),
	})

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
			telegramDao.NewUpload(telegramDB, arweave, minioCli),
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

	args.WebSearchEngine = google.NewSearchEngine(
		gconfig.S.GetString("settings.websearch.google.api_key"),
		gconfig.S.GetString("settings.websearch.google.cx"),
	)

	{ // setup ask_user service
		dial := postgres.DialInfo{
			Addr:   gconfig.S.GetString("settings.db.mcp.addr"),
			DBName: gconfig.S.GetString("settings.db.mcp.db"),
			User:   gconfig.S.GetString("settings.db.mcp.user"),
			Pwd:    gconfig.S.GetString("settings.db.mcp.pwd"),
		}
		if dial.Addr == "" || dial.DBName == "" || dial.User == "" {
			logger.Warn("ask_user database configuration incomplete",
				zap.Bool("addr_empty", dial.Addr == ""),
				zap.Bool("db_empty", dial.DBName == ""),
				zap.Bool("user_empty", dial.User == ""),
			)
		} else if askDB, err := postgres.NewDB(ctx, dial); err != nil {
			logger.Error("new ask_user postgres", zap.Error(err))
		} else if svc, err := askuser.NewService(askDB.DB, logger.Named("ask_user")); err != nil {
			logger.Error("init ask_user service", zap.Error(err))
		} else {
			args.AskUserService = svc
		}
	}

	resolver := web.NewResolver(args)
	web.RunServer(gconfig.Shared.GetString("listen"), resolver)
	return nil
}
