// Package telegram controller
package telegram

import (
	"context"

	"github.com/Laisky/laisky-blog-graphql/library/log"
	"github.com/Laisky/laisky-blog-graphql/library/throttle"

	gconfig "github.com/Laisky/go-config/v2"
	"github.com/Laisky/zap"
)

var (
	telegramRatelimiter *throttle.TelegramThrottle
)

// setupTelegramThrottle installs the alert rate limiter for this process.
//
// Telegram is an optional subsystem. A deployment that does not configure it
// still constructs this controller, because internal/web.NewResolver builds a
// fallback controller whenever cmd/api.go could not create the telegram
// service and the "telegram" task was not requested. Aborting the process here
// would take down GraphQL, MCP and the dedicated HTTP routes over a subsystem
// the operator never asked for, so an unusable configuration is reported and
// the limiter is left absent. TelegramMonitorAlert then refuses to send rather
// than pushing unlimited alerts. The fail-fast contract for an explicitly
// requested telegram task stays in cmd/api.go, which validates it there.
func setupTelegramThrottle(ctx context.Context) {
	cfg := &throttle.TelegramThrottleCfg{
		TotleBurst:       gconfig.Shared.GetInt("settings.telegram.throttle.total_burst"),
		TotleNPerSec:     gconfig.Shared.GetInt("settings.telegram.throttle.total_per_sec"),
		EachTitleNPerSec: gconfig.Shared.GetInt("settings.telegram.throttle.each_title_per_sec"),
		EachTitleBurst:   gconfig.Shared.GetInt("settings.telegram.throttle.each_title_burst"),
	}
	limiter, err := throttle.NewTelegramThrottle(ctx, cfg)
	if err != nil {
		telegramRatelimiter = nil
		log.Logger.Error("telegram alert throttle is unavailable; telegram alerting is disabled",
			zap.Error(err),
			zap.Int("TotleBurst", cfg.TotleBurst),
			zap.Int("TotleNPerSec", cfg.TotleNPerSec),
			zap.Int("EachTitleNPerSec", cfg.EachTitleNPerSec),
			zap.Int("EachTitleBurst", cfg.EachTitleBurst),
		)
		return
	}
	telegramRatelimiter = limiter
}

// ValidateTelegramThrottleConfig reports why the alert rate limiter cannot be
// built, so a caller that requires telegram (the "telegram" task) can fail fast
// at startup instead of discovering the gap on the first alert.
func ValidateTelegramThrottleConfig(ctx context.Context) error {
	_, err := throttle.NewTelegramThrottle(ctx, &throttle.TelegramThrottleCfg{
		TotleBurst:       gconfig.Shared.GetInt("settings.telegram.throttle.total_burst"),
		TotleNPerSec:     gconfig.Shared.GetInt("settings.telegram.throttle.total_per_sec"),
		EachTitleNPerSec: gconfig.Shared.GetInt("settings.telegram.throttle.each_title_per_sec"),
		EachTitleBurst:   gconfig.Shared.GetInt("settings.telegram.throttle.each_title_burst"),
	})
	return err
}
