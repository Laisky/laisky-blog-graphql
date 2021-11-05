package telegram

import (
	"context"

	"laisky-blog-graphql/library/log"
	"laisky-blog-graphql/library/throttle"

	gutils "github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
)

var (
	telegramThrottle *throttle.TelegramThrottle
)

func setupTelegramThrottle(ctx context.Context) {
	var err error
	if telegramThrottle, err = throttle.NewTelegramThrottle(ctx, &throttle.TelegramThrottleCfg{
		TotleBurst:       gutils.Settings.GetInt("settings.telegram.throttle.total_burst"),
		TotleNPerSec:     gutils.Settings.GetInt("settings.telegram.throttle.total_per_sec"),
		EachTitleNPerSec: gutils.Settings.GetInt("settings.telegram.throttle.each_title_per_sec"),
		EachTitleBurst:   gutils.Settings.GetInt("settings.telegram.throttle.each_title_burst"),
	}); err != nil {
		log.Logger.Panic("create telegramThrottle", zap.Error(err),
			zap.Int("TotleBurst", gutils.Settings.GetInt("settings.telegram.throttle.total_burst")),
			zap.Int("TotleNPerSec", gutils.Settings.GetInt("settings.telegram.throttle.total_per_sec")),
			zap.Int("EachTitleNPerSec", gutils.Settings.GetInt("settings.telegram.throttle.each_title_per_sec")),
			zap.Int("EachTitleBurst", gutils.Settings.GetInt("settings.telegram.throttle.each_title_burst")),
		)
	}
}
