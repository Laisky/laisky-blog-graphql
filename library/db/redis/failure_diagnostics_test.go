package redis

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCrawlerFailureDiagnosticWireCompatibility(t *testing.T) {
	task, err := NewHTMLCrawlerTaskFromString(`{"task_id":"0199c118-4229-7000-8000-000000000002","status":"failed","failed_reason":"legacy-safe-reason","failure_stage":"conversion","failure_category":"empty_markdown","url":"https://1.1.1.1/doc"}`)
	require.NoError(t, err)
	require.Equal(t, "conversion", task.FailureStage)
	require.Equal(t, "empty_markdown", task.FailureCategory)
	raw, err := task.ToString()
	require.NoError(t, err)
	var legacy struct {
		Status       string  `json:"status"`
		FailedReason *string `json:"failed_reason"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &legacy))
	require.Equal(t, "failed", legacy.Status)
	require.Equal(t, "legacy-safe-reason", *legacy.FailedReason)
	task, err = NewHTMLCrawlerTaskFromString(`{"status":"failed","failed_reason":"legacy-safe-reason"}`)
	require.NoError(t, err)
	require.Empty(t, task.FailureStage)
	require.Empty(t, task.FailureCategory)
	raw, err = task.ToString()
	require.NoError(t, err)
	require.NotContains(t, raw, "failure_stage")
	require.NotContains(t, raw, "failure_category")
}

func TestCrawlerFailureDiagnosticRejectsMalformedScalarTypes(t *testing.T) {
	for _, raw := range []string{
		`{"failure_stage":{"private-canary":"private-canary"}}`,
		`{"failure_category":["private-canary"]}`,
		`{"failure_stage":123,"failed_reason":"private-canary"}`,
	} {
		_, err := NewHTMLCrawlerTaskFromString(raw)
		require.Error(t, err)
		require.NotContains(t, err.Error(), "canary")
	}
}
