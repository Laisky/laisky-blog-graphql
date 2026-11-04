package log

import (
	"testing"

	"github.com/Laisky/zap/zapcore"
	"github.com/stretchr/testify/require"
)

// TestWebFetchLifecycleFieldsFinite rejects unreviewed context/lifecycle text without affecting other fields.
func TestWebFetchLifecycleFieldsFinite(t *testing.T) {
	for _, tc := range []struct {
		caller, lifecycle string
		accepted          bool
	}{
		{"active", "none", true}, {"canceled", "canceled", true},
		{"deadline_exceeded", "deadline_exceeded", true}, {"active", "mixed", true}, {"active", "unknown", true},
		{"private-caller-canary", "private-error-canary", false},
		{"", "", false},
	} {
		fields := WebFetchDiagnosticFields(WebFetchFailureSummary{ErrorCode: "crawler_result_failed", Stage: "result",
			CallerContextState: tc.caller, ErrorLifecycle: tc.lifecycle, DurationMS: 5})
		values := map[string]string{}
		for _, field := range fields {
			if field.Type == zapcore.StringType {
				values[field.Key] = field.String
			}
		}
		require.Equal(t, "crawler_result_failed", values["error_code"])
		if tc.accepted {
			require.Equal(t, tc.caller, values["caller_context_state"])
			require.Equal(t, tc.lifecycle, values["error_lifecycle"])
		} else {
			require.NotContains(t, values, "caller_context_state")
			require.NotContains(t, values, "error_lifecycle")
		}
	}
}
