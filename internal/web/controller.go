package web

import (
	"context"

	"github.com/Laisky/zap"

	"laisky-blog-graphql/internal/apps/telegram"
	"laisky-blog-graphql/library/log"

	utils "github.com/Laisky/go-utils"
)

var (
	telegramCli      *telegram.Telegram
	telegramThrottle *TelegramThrottle
)

func setupTelegramThrottle(ctx context.Context) {
	var err error
	if telegramThrottle, err = NewTelegramThrottle(ctx, &TelegramThrottleCfg{
		TotleBurst:       utils.Settings.GetInt("settings.telegram.throttle.total_burst"),
		TotleNPerSec:     utils.Settings.GetInt("settings.telegram.throttle.total_per_sec"),
		EachTitleNPerSec: utils.Settings.GetInt("settings.telegram.throttle.each_title_per_sec"),
		EachTitleBurst:   utils.Settings.GetInt("settings.telegram.throttle.each_title_burst"),
	}); err != nil {
		log.Logger.Panic("create telegramThrottle", zap.Error(err),
			zap.Int("TotleBurst", utils.Settings.GetInt("settings.telegram.throttle.total_burst")),
			zap.Int("TotleNPerSec", utils.Settings.GetInt("settings.telegram.throttle.total_per_sec")),
			zap.Int("EachTitleNPerSec", utils.Settings.GetInt("settings.telegram.throttle.each_title_per_sec")),
			zap.Int("EachTitleBurst", utils.Settings.GetInt("settings.telegram.throttle.each_title_burst")),
		)
	}
}

func setupTasks(ctx context.Context) {
	var err error
	for _, task := range utils.Settings.GetStringSlice("tasks") {
		switch task {
		case "telegram":
			log.Logger.Info("enable telegram")
			if telegramCli, err = telegram.NewTelegram(
				ctx,
				monitorDB,
				utils.Settings.GetString("settings.telegram.token"),
				utils.Settings.GetString("settings.telegram.api"),
			); err != nil {
				log.Logger.Panic("new telegram", zap.Error(err))
			}
		default:
			log.Logger.Panic("unknown task", zap.String("task", task))
		}
	}
}

type Controllor struct {
}

func NewControllor() *Controllor {
	return &Controllor{}
}

func (c *Controllor) Run(ctx context.Context) {
	setupDB(ctx)
	setupTasks(ctx)
	setupTelegramThrottle(ctx)
	RunServer(utils.Settings.GetString("addr"))
}
