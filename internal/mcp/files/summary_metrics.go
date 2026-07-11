package files

import (
	"context"

	"github.com/Laisky/zap"
)

// Summary observability is emitted as structured log events, consistent with the rest
// of the mcp_files package (which uses zap, not Prometheus). Each event carries a
// stable `metric` name matching docs/proposals/file_search_file_summaries.md §7.1 so
// the counters can be derived from logs. None of these events include source content,
// summary text, prompts, or credentials (§7.2).

// logSummaryGeneration records the outcome of one summary generation decision.
func (s *Service) logSummaryGeneration(ctx context.Context, plugin string, pub summaryPublication) {
	if !pub.publish {
		return // pure reuse of an existing ready summary; nothing generated
	}
	logger := s.LoggerFromContext(ctx)
	logger.Debug("mcp_files_summary_generation_total",
		zap.String("metric", "mcp_files_summary_generation_total"),
		zap.String("plugin", plugin),
		zap.String("status", string(pub.status)),
		zap.String("source", string(pub.source)),
		zap.Int("word_count", pub.wordCount),
		zap.Bool("model_call", pub.usedModelCall),
		zap.String("error_code", pub.errorCode),
	)
	if pub.status == SummaryStatusDegraded {
		logger.Debug("mcp_files_summary_degraded_total",
			zap.String("metric", "mcp_files_summary_degraded_total"),
			zap.String("plugin", plugin),
			zap.String("reason", pub.errorCode),
		)
	}
}

// summaryStaleDiscard records that a stale worker output was discarded before it could
// overwrite a newer generation. This counter must stay at zero for real overwrites.
func (s *Service) summaryStaleDiscard(ctx context.Context, plugin, project, path string) {
	s.LoggerFromContext(ctx).Debug("mcp_files_summary_stale_discard_total",
		zap.String("metric", "mcp_files_summary_stale_discard_total"),
		zap.String("plugin", plugin),
		zap.String("project", project),
		zap.String("file_path", path),
	)
}

// summaryRefreshOutcome records a SUMMARY_REFRESH terminal or retry transition.
func (s *Service) summaryRefreshOutcome(ctx context.Context, plugin, status string) {
	s.LoggerFromContext(ctx).Debug("mcp_files_summary_refresh_total",
		zap.String("metric", "mcp_files_summary_refresh_total"),
		zap.String("plugin", plugin),
		zap.String("status", status),
	)
}

// summaryMissingHit records a returned search hit that lacked a summary. After
// enforcement this must remain zero.
func (s *Service) summaryMissingHit(ctx context.Context, plugin, project, path string) {
	s.LoggerFromContext(ctx).Debug("mcp_files_summary_missing_hit_total",
		zap.String("metric", "mcp_files_summary_missing_hit_total"),
		zap.String("plugin", plugin),
		zap.String("project", project),
		zap.String("file_path", path),
	)
}
