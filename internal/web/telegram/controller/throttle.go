// Package telegram controller
package telegram

import (
	"context"

	"github.com/Laisky/errors/v2"
	gconfig "github.com/Laisky/go-config/v2"
	"github.com/Laisky/zap"

	"github.com/Laisky/laisky-blog-graphql/library/log"
	"github.com/Laisky/laisky-blog-graphql/library/throttle"
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
	cfg := loadTelegramThrottleCfg()
	limiter, err := throttle.NewTelegramThrottle(ctx, cfg)
	if err != nil {
		telegramRatelimiter = nil
		log.Logger.Error("telegram alert throttle is unavailable; telegram alerting is disabled",
			zap.Error(err),
			zap.Int("TotalBurst", cfg.TotalBurst),
			zap.Int("TotalNPerSec", cfg.TotalNPerSec),
			zap.Int("EachTitleNPerSec", cfg.EachTitleNPerSec),
			zap.Int("EachTitleBurst", cfg.EachTitleBurst),
		)
		return
	}
	telegramRatelimiter = limiter
}

// ValidateTelegramThrottleConfig reports why the alert rate limiter cannot be
// built, so a caller that requires telegram (the "telegram" task) can fail fast
// at startup instead of discovering the gap on the first alert. It only checks
// the settings: it builds no limiter, so nothing outlives the call.
func ValidateTelegramThrottleConfig(_ context.Context) error {
	if err := loadTelegramThrottleCfg().Validate(); err != nil {
		return errors.Wrap(err, "validate telegram throttle config")
	}
	return nil
}

// loadTelegramThrottleCfg reads the alert rate limiter settings. It is the one
// place that maps configuration keys to the throttle, for both setup and validation.
func loadTelegramThrottleCfg() *throttle.TelegramThrottleCfg {
	return &throttle.TelegramThrottleCfg{
		TotalBurst:       gconfig.Shared.GetInt("settings.telegram.throttle.total_burst"),
		TotalNPerSec:     gconfig.Shared.GetInt("settings.telegram.throttle.total_per_sec"),
		EachTitleNPerSec: gconfig.Shared.GetInt("settings.telegram.throttle.each_title_per_sec"),
		EachTitleBurst:   gconfig.Shared.GetInt("settings.telegram.throttle.each_title_burst"),
	}
}
