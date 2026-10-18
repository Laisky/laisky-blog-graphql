package log

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Laisky/zap"
	"github.com/stretchr/testify/require"
)

// TestOTLPExternalAcceptance exercises the production configuration and init hook
// only when a caller explicitly supplies a disposable authenticated TLS receiver.
func TestOTLPExternalAcceptance(t *testing.T) {
	if os.Getenv("OTLP_LOG_ACCEPTANCE") != "1" {
		t.Skip("set OTLP_LOG_ACCEPTANCE=1 for explicit isolated TLS delivery acceptance")
	}
	require.NotEmpty(t, os.Getenv("APP_LOG_OTLP_ENDPOINT"))
	shutdown, err := ConfigureOTLP(context.Background())
	require.NoError(t, err)
	Logger.Info("synthetic operational refresh completed",
		zap.String("stage", "cache"), zap.String("future_revision", "parser-v2"), zap.Int("channel_count", 17),
		zap.Float64("duration_ms", 12.5), zap.Bool("cache_ready", true),
		zap.String("api_key", "SYNTHETIC_EXTERNAL_API_SECRET"),
		zap.String("authorization", "Bearer SYNTHETIC_EXTERNAL_AUTH_SECRET"),
		zap.String("url", "https://user:SYNTHETIC_EXTERNAL_URL_SECRET@example.invalid/private?token=SYNTHETIC_EXTERNAL_QUERY_SECRET"),
		zap.String("request_body", "SYNTHETIC_EXTERNAL_REQUEST_SECRET"),
		zap.Int("status_code", 299), zap.Bool("success", true))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, shutdown(ctx))
	stats := SnapshotOTLP()
	require.Equal(t, uint64(1), stats.Emitted)
	require.Equal(t, uint64(1), stats.ExportedRecords)
	require.Zero(t, stats.FailedExportRecords)
	require.Zero(t, stats.Unresolved)
}
