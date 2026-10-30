package log

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Laisky/zap"
	"github.com/Laisky/zap/zapcore"
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
		zap.String("api_key", "external-private-api-canary"),
		zap.String("authorization", "Bearer external-private-auth-canary"),
		zap.String("target_url", "https://user:external-private-url-canary@example.test/private?token=external-private-query-canary"),
		zap.String("request_body", "external-private-body-canary"), zap.Int("status_code", 299), zap.Bool("success", true),
		zap.Object("object", externalFidelityObject{}), zap.Array("array", externalFidelityArray{}),
		zap.Reflect("null_value", nil), zap.Reflect("empty_object", map[string]any{}), zap.Reflect("empty_array", []any{}),
		zap.String("service.name", "application-service"), zap.String("host.name", "application-host"), zap.String("_msg", "application-field"),
		zap.Any("nested", map[string]any{"payload": "SYNTHETIC_EXTERNAL_NESTED_BODY", "count": 17, "nullable": nil, "empty": map[string]any{}, "items": []any{"one", 2, true}}),
		zap.Namespace("namespace"), zap.String("payload", "synthetic-namespace-canary"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, shutdown(ctx))
	stats := SnapshotOTLP()
	require.Equal(t, uint64(1), stats.Emitted)
	require.Equal(t, uint64(1), stats.ExportedRecords)
	require.Zero(t, stats.FailedExportRecords)
	require.Zero(t, stats.Unresolved)
	require.Zero(t, stats.Rejected)
}

// externalFidelityObject supplies the common native Object acceptance shape.
type externalFidelityObject struct{}

// MarshalLogObject emits synthetic application fields shared with the Ramjet fixture.
func (externalFidelityObject) MarshalLogObject(encoder zapcore.ObjectEncoder) error {
	encoder.AddString("api_key", "synthetic-object-canary")
	encoder.AddInt("count", 7)
	return nil
}

// externalFidelityArray supplies the common native heterogeneous Array acceptance shape.
type externalFidelityArray struct{}

// MarshalLogArray retains ordered primitive and object elements in the external fixture.
func (externalFidelityArray) MarshalLogArray(encoder zapcore.ArrayEncoder) error {
	encoder.AppendString("synthetic-array-canary")
	encoder.AppendInt(9)
	return encoder.AppendObject(externalFidelityObject{})
}
