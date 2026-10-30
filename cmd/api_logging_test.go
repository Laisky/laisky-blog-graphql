package cmd

import (
	"context"
	"testing"

	"github.com/Laisky/errors/v2"
	"github.com/stretchr/testify/require"
)

// TestAPIOptionalLoggingPreservesCommandContextAndFailure verifies the isolated logging lifecycle contract.
func TestAPIOptionalLoggingPreservesCommandContextAndFailure(t *testing.T) {
	t.Setenv("APP_LOG_OTLP_ENDPOINT", "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	expected := errors.New("synthetic API startup failure")
	called := false
	err := runAPIWithLogging(ctx, func(got context.Context) error {
		called = true
		require.ErrorIs(t, got.Err(), context.Canceled)
		return expected
	})
	require.True(t, called)
	require.ErrorIs(t, err, expected)
}

// TestAPIRejectsUnsafeLoggingBeforeStarting verifies the isolated logging lifecycle contract.
func TestAPIRejectsUnsafeLoggingBeforeStarting(t *testing.T) {
	t.Setenv("APP_LOG_OTLP_ENDPOINT", "http://127.0.0.1:4318/v1/logs")
	called := false
	err := runAPIWithLogging(context.Background(), func(context.Context) error { called = true; return nil })
	require.Error(t, err)
	require.False(t, called)
}
