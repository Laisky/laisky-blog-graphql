package telegram

import (
	"context"
	"testing"

	gconfig "github.com/Laisky/go-config/v2"
	"github.com/stretchr/testify/require"
)

// throttleConfigKeys lists every setting the alert rate limiter reads.
var throttleConfigKeys = []string{
	"settings.telegram.throttle.total_burst",
	"settings.telegram.throttle.total_per_sec",
	"settings.telegram.throttle.each_title_per_sec",
	"settings.telegram.throttle.each_title_burst",
}

// resetThrottleConfig clears the throttle settings and the package-level limiter
// so each case starts from a known configuration state.
func resetThrottleConfig(t *testing.T) {
	t.Helper()
	previous := telegramRatelimiter
	for _, key := range throttleConfigKeys {
		gconfig.Shared.Set(key, 0)
	}
	telegramRatelimiter = nil
	t.Cleanup(func() {
		for _, key := range throttleConfigKeys {
			gconfig.Shared.Set(key, 0)
		}
		telegramRatelimiter = previous
	})
}

// TestNewTelegramWithoutThrottleConfigDoesNotPanic reproduces the startup crash
// that hit any deployment which does not configure telegram at all.
//
// cmd/api.go leaves TelegramCtl nil when the telegram databases or service are
// unavailable and the "telegram" task was NOT requested, and web.NewResolver
// then builds a fallback controller. Constructing that controller must not kill
// the process: the documented contract is to log an error and keep serving
// every other interface.
func TestNewTelegramWithoutThrottleConfigDoesNotPanic(t *testing.T) {
	resetThrottleConfig(t)

	require.NotPanics(t, func() {
		controller := NewTelegram(context.Background(), nil)
		require.NotNil(t, controller)
	}, "an unconfigured telegram throttle must not abort server startup")
	require.Nil(t, telegramRatelimiter, "no limiter may be fabricated from absent configuration")
}

// TestNewTelegramWithInvalidThrottleConfigDoesNotPanic covers a present but
// unusable configuration, which must degrade the same way rather than crash.
func TestNewTelegramWithInvalidThrottleConfigDoesNotPanic(t *testing.T) {
	resetThrottleConfig(t)
	gconfig.Shared.Set("settings.telegram.throttle.total_per_sec", 10)
	gconfig.Shared.Set("settings.telegram.throttle.total_burst", 1) // burst < per-sec
	gconfig.Shared.Set("settings.telegram.throttle.each_title_per_sec", 5)
	gconfig.Shared.Set("settings.telegram.throttle.each_title_burst", 5)

	require.NotPanics(t, func() {
		require.NotNil(t, NewTelegram(context.Background(), nil))
	})
	require.Nil(t, telegramRatelimiter)
}

// TestNewTelegramWithValidThrottleConfig keeps the working path intact: a valid
// configuration must still install a real limiter.
func TestNewTelegramWithValidThrottleConfig(t *testing.T) {
	resetThrottleConfig(t)
	gconfig.Shared.Set("settings.telegram.throttle.total_per_sec", 10)
	gconfig.Shared.Set("settings.telegram.throttle.total_burst", 20)
	gconfig.Shared.Set("settings.telegram.throttle.each_title_per_sec", 5)
	gconfig.Shared.Set("settings.telegram.throttle.each_title_burst", 10)

	require.NotNil(t, NewTelegram(context.Background(), nil))
	require.NotNil(t, telegramRatelimiter)
	require.True(t, telegramRatelimiter.Allow("alert-type"))
}

// TestAlertWithoutThrottleReturnsCleanError pins the request-path half: with no
// limiter installed, the alert mutation must return an error instead of
// dereferencing nil inside the resolver.
func TestAlertWithoutThrottleReturnsCleanError(t *testing.T) {
	resetThrottleConfig(t)
	NewTelegram(context.Background(), nil)
	require.Nil(t, telegramRatelimiter)

	resolver := NewMutationResolver(nil)
	require.NotPanics(t, func() {
		alert, err := resolver.TelegramMonitorAlert(context.Background(), "alert-type", "token", "message")
		require.Error(t, err)
		require.Nil(t, alert)
	})
}
