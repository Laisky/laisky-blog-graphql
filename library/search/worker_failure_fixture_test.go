package search

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	rlibs "github.com/Laisky/laisky-blog-graphql/library/db/redis"
)

// TestWorkerFailureActualEncoderFixture decodes the sibling worker's real result-encoder output.
func TestWorkerFailureActualEncoderFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/crawler_failure_diagnostics.json")
	require.NoError(t, err)
	task, err := rlibs.NewHTMLCrawlerTaskFromString(string(raw))
	require.NoError(t, err)
	require.Equal(t, rlibs.TaskStatusFailed, task.Status)
	require.NotNil(t, task.Egress)
	require.Nil(t, task.EgressReceipt, "failure diagnostics are not connection proof")
	require.Empty(t, task.APIKey)
	require.Empty(t, task.ResultHTML)
	require.Empty(t, task.ResultMarkdown)
	require.Equal(t, "conversion", task.FailureStage)
	require.Equal(t, "conversion_failed", task.FailureCategory)
	diagnostic := FetchDiagnostic(failedCrawlerTask(task.TaskID, task.Url, *task.Egress, task.OutputMarkdown, task))
	require.Equal(t, "crawler_failed", diagnostic.ErrorCode)
	require.Equal(t, "render", diagnostic.Stage)
	require.Equal(t, "conversion", diagnostic.WorkerStage)
	require.Equal(t, "conversion_failed", diagnostic.WorkerCategory)

	var legacy deployed3952CrawlerTask
	require.NoError(t, json.Unmarshal(raw, &legacy), "old decoder ignores additive keys")
	require.Equal(t, task.TaskID, legacy.TaskID)
	require.Equal(t, task.Url, legacy.URL)
	require.Equal(t, task.Status, legacy.Status)
	require.Equal(t, task.OutputMarkdown, legacy.OutputMarkdown)
	require.Equal(t, "conversion_failed", *legacy.FailedReason)
	legacyRaw, err := json.Marshal(legacy)
	require.NoError(t, err)
	oldTask, err := rlibs.NewHTMLCrawlerTaskFromString(string(legacyRaw))
	require.NoError(t, err)
	require.Empty(t, oldTask.FailureStage)
	require.Empty(t, oldTask.FailureCategory)
	generic := FetchDiagnostic(failedCrawlerTask(oldTask.TaskID, oldTask.Url, *oldTask.Egress, oldTask.OutputMarkdown, oldTask))
	require.Equal(t, "crawler_failed", generic.ErrorCode)
	require.Equal(t, "render", generic.Stage)
	require.Empty(t, generic.WorkerStage, "legacy FailedReason is never guessed into a stage")
	require.Empty(t, generic.WorkerCategory)
}
