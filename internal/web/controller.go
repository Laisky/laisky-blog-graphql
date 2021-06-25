package web

import (
	"context"

	"laisky-blog-graphql/internal/global"
	"laisky-blog-graphql/library/log"

	gutils "github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
)

var (
	telegramThrottle *TelegramThrottle
)

func setupTelegramThrottle(ctx context.Context) {
	var err error
	if telegramThrottle, err = NewTelegramThrottle(ctx, &TelegramThrottleCfg{
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

type Controllor struct {
}

func NewControllor() *Controllor {
	return &Controllor{}
}

func (c *Controllor) Run(ctx context.Context) {
	global.SetupDB(ctx)
	global.SetupServices(ctx)

	setupTelegramThrottle(ctx)
	RunServer(gutils.Settings.GetString("addr"))
}
