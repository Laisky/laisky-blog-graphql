package search

import (
	"context"
	"time"

	"github.com/Laisky/errors/v2"

	rlibs "github.com/Laisky/laisky-blog-graphql/library/db/redis"
)

// FetchDynamicURLContent is a wrapper for submit & fetch dynamic url content
func FetchDynamicURLContent(ctx context.Context, rdb *rlibs.DB, url string) ([]byte, error) {
	// submit task
	taskID, err := rdb.AddHTMLCrawlerTask(ctx, url)
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
