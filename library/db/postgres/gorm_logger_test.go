package postgres

import (
	"fmt"
	"strings"
	"testing"

	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/require"
)

// TestSanitizeLoggedSQLParamVector verifies vector parameters are summarized in logs.
func TestSanitizeLoggedSQLParamVector(t *testing.T) {
	vector := make([]float32, 0, 32)
	for idx := 0; idx < 32; idx++ {
		vector = append(vector, float32(idx)/10)
	}

	sanitized := sanitizeLoggedSQLParam(pgvector.NewVector(vector), 128, 4)
	result, ok := sanitized.(string)
	require.True(t, ok)
	require.Contains(t, result, "<vector:dim=32")
	require.Contains(t, result, "truncated=true")
}

// TestSanitizeLoggedSQLParamVectorLiteralString verifies vector-like literals are truncated.
func TestSanitizeLoggedSQLParamVectorLiteralString(t *testing.T) {
	param := "[0.1,0.2,0.3,0.4,0.5,0.6]"
	sanitized := sanitizeLoggedSQLParam(param, 10, 4)
	result, ok := sanitized.(string)
	require.True(t, ok)
	require.Contains(t, result, "<truncated:len=")
	require.LessOrEqual(t, len(result), 40)
}

// TestSanitizeLoggedSQLParams verifies params filtering sanitizes oversized values.
func TestSanitizeLoggedSQLParams(t *testing.T) {
	vector := make([]float32, 0, 16)
	for idx := 0; idx < 16; idx++ {
		vector = append(vector, float32(idx))
	}
	vectorLiteral := "[" + strings.Repeat("0.123456789,", 40) + "0.987654321]"
	longString := fmt.Sprintf("%0257d", 0)

	filteredParams := sanitizeLoggedSQLParams(pgvector.NewVector(vector), vectorLiteral, longString)
	require.Len(t, filteredParams, 3)
	require.Contains(t, filteredParams[0], "<vector:dim=16")
	require.Contains(t, filteredParams[1], "<truncated:len=")
	require.Equal(t, "<string:len=257,truncated>", filteredParams[2])
}
