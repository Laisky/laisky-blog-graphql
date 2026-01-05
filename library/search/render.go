package search

import (
	"context"
	"time"

	"github.com/Laisky/errors/v2"

	rlibs "github.com/Laisky/laisky-blog-graphql/library/db/redis"
)

type htmlToMarkdownConverter interface {
	ConvertString(string) (string, error)
}

// FetchDynamicURLContent is a wrapper for submit & fetch dynamic url content.
// When apiKey is not empty and outputMarkdown is true, it converts the fetched HTML body
// to markdown. If conversion fails, it returns the raw HTML body unchanged.
func FetchDynamicURLContent(ctx context.Context, rdb *rlibs.DB, url, apiKey string, outputMarkdown bool) ([]byte, error) {
	// submit task
	taskID, err := rdb.AddHTMLCrawlerTaskWithOptions(ctx, url, apiKey, outputMarkdown)
	if err != nil {
		return nil, errors.Wrap(err, "submit task")
	}

	// fetch task result
	for {
		task, err := rdb.GetHTMLCrawlerTaskResult(ctx, taskID)
		if err != nil {
			return nil, errors.Wrap(err, "get task result")
		}

		switch task.Status {
		case rlibs.TaskStatusSuccess:
			return task.ResultHTML, nil
		case rlibs.TaskStatusPending,
			rlibs.TaskStatusRunning:
			time.Sleep(time.Second)
			continue
		case rlibs.TaskStatusFailed:
			return nil, errors.Errorf("task failed at %s for reason %q",
				*task.FinishedAt, *task.FailedReason)
		default:
			return nil, errors.Errorf("unknown task status %q", task.Status)
		}
	}
}
