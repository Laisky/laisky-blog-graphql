package controller

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	rlibs "github.com/Laisky/laisky-blog-graphql/library/db/redis"
)

// TestNewGeneralLLMStormTask verifies conversion of Redis LLM storm task into GraphQL model.
func TestNewGeneralLLMStormTask(t *testing.T) {
	t.Parallel()

	raw := `{
		"task_id": "task-1",
		"created_at": "2025-01-01T12:34:56Z",
		"status": "success",
		"failed_reason": null,
		"finished_at": "2025-01-01T12:35:56Z",
		"prompt": "prompt-text",
		"api_key": "api-key",
		"result_article": "article-content",
		"runner": "worker-1",
		"result_references": {
			"url_to_unified_index": {"https://example.com": 1},
			"url_to_info": {
				"https://example.com": {
					"url": "https://example.com",
					"description": "desc",
					"snippets": ["snippet"],
					"title": "title",
					"meta": {"query": "prompt"},
					"citation_uuid": 42
				}
			}
		}
	}`

	task, err := rlibs.NewLLMStormTaskFromString(raw)
	require.NoError(t, err)

	converted, err := newGeneralLLMStormTask(task)
	require.NoError(t, err)

	require.Equal(t, "task-1", converted.TaskID)
	require.Equal(t, "prompt-text", converted.Prompt)
	require.Equal(t, "api-key", converted.APIKey)
	require.Equal(t, "success", converted.Status)
	require.Equal(t, "worker-1", converted.Runner)
	require.NotNil(t, converted.ResultArticle)
	require.Equal(t, "article-content", *converted.ResultArticle)
	require.Nil(t, converted.FailedReason)
	require.Equal(t, time.Date(2025, 1, 1, 12, 34, 56, 0, time.UTC), converted.CreatedAt.GetTime())
	require.NotNil(t, converted.FinishedAt)
	require.Equal(t, time.Date(2025, 1, 1, 12, 35, 56, 0, time.UTC), converted.FinishedAt.GetTime())
	require.NotNil(t, converted.ResultReferences)

	var refs map[string]any
	require.NoError(t, json.Unmarshal([]byte(string(*converted.ResultReferences)), &refs))
	require.Contains(t, refs, "url_to_unified_index")
	require.Contains(t, refs, "url_to_info")
}

// TestNewGeneralHTMLCrawlerTask verifies conversion of Redis HTML crawler task into GraphQL model.
func TestNewGeneralHTMLCrawlerTask(t *testing.T) {
	t.Parallel()

	task := rlibs.NewHTMLCrawlerTask("https://example.com")
	task.TaskID = "task-2"
	task.CreatedAt = time.Date(2025, 2, 2, 1, 2, 3, 0, time.UTC)
	task.Status = rlibs.TaskStatusRunning
	failed := "temporary"
	task.FailedReason = &failed
	finished := time.Date(2025, 2, 2, 1, 3, 3, 0, time.UTC)
	task.FinishedAt = &finished
	payload := []byte("<html></html>")
	task.ResultHTML = payload

	converted, err := newGeneralHTMLCrawlerTask(task)
	require.NoError(t, err)

	require.Equal(t, "task-2", converted.TaskID)
	require.Equal(t, "https://example.com", converted.URL)
	require.Equal(t, rlibs.TaskStatusRunning, converted.Status)
	require.Equal(t, time.Date(2025, 2, 2, 1, 2, 3, 0, time.UTC), converted.CreatedAt.GetTime())
	require.NotNil(t, converted.FinishedAt)
	require.Equal(t, time.Date(2025, 2, 2, 1, 3, 3, 0, time.UTC), converted.FinishedAt.GetTime())
	require.NotNil(t, converted.ResultHTMLB64)
	require.Equal(t, base64.StdEncoding.EncodeToString(payload), *converted.ResultHTMLB64)
}
