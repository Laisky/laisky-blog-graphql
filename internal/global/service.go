package global

import (
	"context"

	"laisky-blog-graphql/internal/web/blog"
	"laisky-blog-graphql/internal/web/general"
	"laisky-blog-graphql/internal/web/telegram"
	"laisky-blog-graphql/internal/web/twitter"
	"laisky-blog-graphql/library/log"

	gutils "github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
)

var (
	BlogSvc     *blog.Service
	TwitterSvc  *twitter.Service
	TelegramSvc *telegram.Service
	GeneralSvc  *general.Service
)

func SetupServices(ctx context.Context) {
	var err error
	for _, task := range gutils.Settings.GetStringSlice("tasks") {
		switch task {
		case "telegram":
			log.Logger.Info("enable telegram")
			if TelegramSvc, err = telegram.NewService(
				ctx,
				MonitorDB,
				gutils.Settings.GetString("settings.telegram.token"),
				gutils.Settings.GetString("settings.telegram.api"),
			); err != nil {
				log.Logger.Panic("new telegram", zap.Error(err))
			}
		default:
			log.Logger.Panic("unknown task", zap.String("task", task))
		}
	}

	BlogSvc = blog.NewService(BlogDB)
	TwitterSvc = twitter.NewService(TwitterDB)
	GeneralSvc = general.NewService(GeneralDB)
}
