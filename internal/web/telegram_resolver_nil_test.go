package web

import (
	"testing"

	gconfig "github.com/Laisky/go-config/v2"
	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/library/models"
)

// TestTelegramResolversWithoutTelegramServiceFailCleanly reproduces a typed-nil
// service: when the Telegram service failed to start, ResolverArgs.TelegramSvc
// is a nil *Telegram, and passing it on as service.Interface made a non-nil
// interface. The resolvers' nil-service guard then missed it, and the first
// call into the service dereferenced nil instead of returning the documented
// "telegram service not initialized" error.
func TestTelegramResolversWithoutTelegramServiceFailCleanly(t *testing.T) {
	keys := []string{
		"settings.telegram.throttle.total_burst", "settings.telegram.throttle.total_per_sec",
		"settings.telegram.throttle.each_title_per_sec", "settings.telegram.throttle.each_title_burst",
	}
	for _, key := range keys {
		prior := gconfig.Shared.Get(key)
		t.Cleanup(func() { gconfig.Shared.Set(key, prior) })
		gconfig.Shared.Set(key, 10)
	}

	resolver := NewResolver(ResolverArgs{})
	require.NotPanics(t, func() {
		_, err := resolver.Mutation().TelegramMonitorAlert(t.Context(), "alert_type", "token", "body")
		require.ErrorContains(t, err, "telegram service not initialized")
	})
	require.NotPanics(t, func() {
		_, err := resolver.Query().TelegramAlertTypes(t.Context(), &models.Pagination{Page: 0, Size: 10}, "")
		require.ErrorContains(t, err, "telegram service not initialized")
	})
}
