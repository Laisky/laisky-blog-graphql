package crawleregress

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWorkerFailureDiagnosticPairs(t *testing.T) {
	pairs := map[string][]string{
		"admission":    {"policy_missing", "policy_invalid", "unsupported_proxy", "task_missing"},
		"render":       {"render_failed", "challenge", "empty_body", "result_too_large"},
		"verification": {"evidence_rejected"},
		"conversion":   {"conversion_failed", "empty_body", "empty_markdown", "result_too_large"},
	}
	for stage, categories := range pairs {
		for _, category := range append(categories, "canceled", "timeout", "worker_failed") {
			approvedStage, approvedCategory := WorkerFailureDiagnostic(stage, category)
			require.Equal(t, stage, approvedStage)
			require.Equal(t, category, approvedCategory)
		}
	}
	for _, pair := range [][2]string{
		{"", "challenge"}, {"render", ""}, {"render", "conversion_failed"}, {"verification", "empty_markdown"},
		{"private-canary", "challenge"}, {"render", strings.Repeat("private-canary", 4096)},
		{"transport", "timeout"}, {"render", "tls timeout challenge private-canary"},
	} {
		stage, category := WorkerFailureDiagnostic(pair[0], pair[1])
		require.Empty(t, stage)
		require.Empty(t, category)
	}
}
