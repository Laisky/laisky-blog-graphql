package search

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	rlibs "github.com/Laisky/laisky-blog-graphql/library/db/redis"
)

// TestCrawlerFailureContextActualWorkerEncoder consumes unchanged bytes emitted
// by the real worker terminal encoder, rather than a separately invented DTO.
func TestCrawlerFailureContextActualWorkerEncoder(t *testing.T) {
	raw, err := os.ReadFile("testdata/crawler_failure_context.json")
	require.NoError(t, err)
	task, err := rlibs.NewHTMLCrawlerTaskFromString(string(raw))
	require.NoError(t, err)
	require.Equal(t, rlibs.TaskStatusFailed, task.Status)
	require.NotNil(t, task.Egress)
	require.Empty(t, task.ResultHTML)
	require.Empty(t, task.ResultMarkdown)
	require.Empty(t, task.APIKey)
	diagnostic := FetchDiagnostic(failedCrawlerTask(task.TaskID, task.Url, *task.Egress, task.OutputMarkdown, task))
	require.Equal(t, "crawler_failed", diagnostic.ErrorCode)
	require.Equal(t, "render", diagnostic.Stage)
	require.Equal(t, "conversion", diagnostic.WorkerStage)
	require.Equal(t, "empty_markdown", diagnostic.WorkerCategory)
	observed := diagnostic.WorkerContext
	require.NotNil(t, observed)
	require.Equal(t, "https", observed.TargetScheme)
	require.Equal(t, "example.com", observed.TargetHostname)
	require.EqualValues(t, 40, *observed.RenderedHTMLBytes)
	require.EqualValues(t, 27, *observed.ConversionInputBytes)
	require.EqualValues(t, 0, *observed.ConversionOutputBytes)
	require.EqualValues(t, 0, *observed.ConversionOutputTrimmedBytes)
	require.EqualValues(t, 7, *observed.BodyTextRunes)
	require.EqualValues(t, 1, *observed.BodyTextNodes)
	require.Equal(t, "html-to-markdown/v2", observed.Converter)
	require.Nil(t, observed.UpstreamStatus)
	require.Empty(t, observed.FinalHostname)
	var legacy deployed3952CrawlerTask
	require.NoError(t, json.Unmarshal(raw, &legacy), "old deployed decoder ignores additive context")
	require.Equal(t, task.TaskID, legacy.TaskID)
	require.Equal(t, task.Url, legacy.URL)
	require.Equal(t, task.Status, legacy.Status)
	legacyRaw, err := json.Marshal(legacy)
	require.NoError(t, err)
	legacyTask, err := rlibs.NewHTMLCrawlerTaskFromString(string(legacyRaw))
	require.NoError(t, err)
	require.Nil(t, legacyTask.FailureContext, "absent legacy fields are not fabricated")
}
