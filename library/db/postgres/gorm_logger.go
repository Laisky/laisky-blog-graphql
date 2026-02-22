package postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/pgvector/pgvector-go"
	gormLogger "gorm.io/gorm/logger"
)

const (
	defaultMaxLoggedParamLength = 256
	defaultVectorPreviewDims    = 8
)

// truncatingParamsLogger filters oversized SQL parameters before GORM prints SQL logs.
type truncatingParamsLogger struct {
	gormLogger.Interface
	maxLoggedParamLength int
	vectorPreviewDims    int
}

// ParamsFilter truncates vector-like and oversized parameter values to keep SQL logs concise.
func (l *truncatingParamsLogger) ParamsFilter(_ context.Context, sql string, params ...any) (string, []any) {
	if len(params) == 0 {
		return sql, params
	}

	filtered := make([]any, len(params))
	for idx, param := range params {
		filtered[idx] = sanitizeLoggedSQLParam(param, l.maxLoggedParamLength, l.vectorPreviewDims)
	}

	return sql, filtered
}

// newTruncatingParamsLogger wraps a GORM logger with parameter truncation.
func newTruncatingParamsLogger(base gormLogger.Interface) gormLogger.Interface {
	return &truncatingParamsLogger{
		Interface:            base,
		maxLoggedParamLength: defaultMaxLoggedParamLength,
		vectorPreviewDims:    defaultVectorPreviewDims,
	}
}

// sanitizeLoggedSQLParam converts oversized parameter values into compact log-safe summaries.
func sanitizeLoggedSQLParam(param any, maxLoggedParamLength, vectorPreviewDims int) any {
	switch value := param.(type) {
	case pgvector.Vector:
		return summarizeVectorForLog(value.Slice(), vectorPreviewDims)
	case string:
		if isVectorLikeLiteral(value) {
			return truncateStringForLog(value, maxLoggedParamLength)
		}
		if len(value) > maxLoggedParamLength {
			return fmt.Sprintf("<string:len=%d,truncated>", len(value))
		}
		return value
	case []byte:
		if len(value) > maxLoggedParamLength {
			return fmt.Sprintf("<bytes:len=%d,truncated>", len(value))
		}
		return value
	default:
		return param
	}
}

// summarizeVectorForLog returns a compact vector summary including dimensionality and preview values.
func summarizeVectorForLog(vector []float32, previewDims int) string {
	if previewDims <= 0 {
		previewDims = defaultVectorPreviewDims
	}

	previewCount := previewDims
	if len(vector) < previewCount {
		previewCount = len(vector)
	}

	preview := make([]float32, 0, previewCount)
	for idx := 0; idx < previewCount; idx++ {
		preview = append(preview, vector[idx])
	}

	return fmt.Sprintf("<vector:dim=%d,preview=%v,truncated=%t>", len(vector), preview, len(vector) > previewCount)
}

// truncateStringForLog shortens a string and appends metadata about the original length.
func truncateStringForLog(raw string, maxLoggedParamLength int) string {
	if maxLoggedParamLength <= 0 || len(raw) <= maxLoggedParamLength {
		return raw
	}
	return fmt.Sprintf("%s...<truncated:len=%d>", raw[:maxLoggedParamLength], len(raw))
}

// isVectorLikeLiteral checks whether a SQL parameter string resembles a vector literal.
func isVectorLikeLiteral(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) < 4 {
		return false
	}
	if !strings.HasPrefix(trimmed, "[") || !strings.HasSuffix(trimmed, "]") {
		return false
	}
	return strings.Contains(trimmed, ",")
}
