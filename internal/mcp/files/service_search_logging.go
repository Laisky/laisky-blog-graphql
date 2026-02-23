package files

import (
	"context"

	"github.com/Laisky/zap"
)

// searchStageMetrics stores one search stage's execution diagnostics.
type searchStageMetrics struct {
	Stage       string
	Engine      string
	DurationMS  int64
	ResultCount int
	Err         error
}

// logSearchStage emits structured per-stage diagnostics for file search.
func (s *Service) logSearchStage(ctx context.Context, project, pathPrefix string, stage searchStageMetrics) {
	logger := s.LoggerFromContext(ctx)
	fields := []zap.Field{
		zap.String("project", project),
		zap.String("path_prefix", pathPrefix),
		zap.String("stage", stage.Stage),
		zap.String("engine", stage.Engine),
		zap.Int64("duration_ms", stage.DurationMS),
		zap.Int("result_count", stage.ResultCount),
		zap.Bool("has_error", stage.Err != nil),
	}
	if stage.Err != nil {
		fields = append(fields, zap.Error(stage.Err))
	}
	logger.Debug("file search stage completed", fields...)
}

// lexicalSearchEngineName returns the lexical backend identifier for diagnostics.
func (s *Service) lexicalSearchEngineName() string {
	if s.isPostgres {
		return "postgres_tsvector"
	}
	return "in_memory_lexical"
}

// semanticSearchEngineName returns the semantic backend identifier for diagnostics.
func (s *Service) semanticSearchEngineName() string {
	if s.isPostgres {
		return "pgvector"
	}
	return "in_memory_cosine"
}
