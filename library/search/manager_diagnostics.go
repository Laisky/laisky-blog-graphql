package search

import (
	"context"
	"time"

	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"

	"github.com/Laisky/laisky-blog-graphql/library/search/diagnostics"
)

func logEngineInvocation(logger logSDK.Logger, engine Engine, tier, attempt int) {
	if logger == nil {
		return
	}
	logger.Debug("search manager invoking engine",
		zap.String("provider", diagnostics.Normalize(diagnostics.Diagnostic{Provider: engine.Type()}).Provider),
		zap.Int("tier", tier), zap.Int("attempt", attempt))
}

func logEngineSuccess(logger logSDK.Logger, engine Engine, tier, attempt int) {
	if logger == nil {
		return
	}
	logger.Info("search manager succeeded",
		zap.String("provider", diagnostics.Normalize(diagnostics.Diagnostic{Provider: engine.Type()}).Provider),
		zap.Int("tier", tier), zap.Int("attempt", attempt))
}

func logEngineFailure(ctx context.Context, logger logSDK.Logger, d diagnostics.Diagnostic, tier, attempt int, elapsed time.Duration) {
	if logger == nil {
		return
	}
	logger.Warn("search engine failed", zap.String("provider", d.Provider), zap.Int("tier", tier), zap.Int("attempt", attempt),
		zap.String("stage", d.Stage), zap.String("category", d.Category), zap.Int("status", d.Status),
		zap.Int64("duration_ms", min(max(elapsed.Milliseconds(), 0), 24*60*60*1000)), zap.String("search_request_id", diagnostics.RequestID(ctx)))
}
