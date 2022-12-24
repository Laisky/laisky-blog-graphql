package telegram

import (
	"context"

	"github.com/Laisky/laisky-blog-graphql/library/log"
	"github.com/Laisky/laisky-blog-graphql/library/throttle"

	gconfig "github.com/Laisky/go-config/v2"
	"github.com/Laisky/zap"
)

var (
	telegramThrottle *throttle.TelegramThrottle
)

func setupTelegramThrottle(ctx context.Context) {
	var err error
	if telegramThrottle, err = throttle.NewTelegramThrottle(ctx, &throttle.TelegramThrottleCfg{
		TotleBurst:       gconfig.Shared.GetInt("settings.telegram.throttle.total_burst"),
		TotleNPerSec:     gconfig.Shared.GetInt("settings.telegram.throttle.total_per_sec"),
		EachTitleNPerSec: gconfig.Shared.GetInt("settings.telegram.throttle.each_title_per_sec"),
		EachTitleBurst:   gconfig.Shared.GetInt("settings.telegram.throttle.each_title_burst"),
	}); err != nil {
		log.Logger.Panic("create telegramThrottle", zap.Error(err),
			zap.Int("TotleBurst", gconfig.Shared.GetInt("settings.telegram.throttle.total_burst")),
			zap.Int("TotleNPerSec", gconfig.Shared.GetInt("settings.telegram.throttle.total_per_sec")),
			zap.Int("EachTitleNPerSec", gconfig.Shared.GetInt("settings.telegram.throttle.each_title_per_sec")),
			zap.Int("EachTitleBurst", gconfig.Shared.GetInt("settings.telegram.throttle.each_title_burst")),
		)
	}
}
