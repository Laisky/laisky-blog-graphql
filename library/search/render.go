package search

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/Laisky/errors/v2"
	gmw "github.com/Laisky/gin-middlewares/v7"
	"github.com/Laisky/zap"

	rlibs "github.com/Laisky/laisky-blog-graphql/library/db/redis"
)

// type htmlToMarkdownConverter interface {
// 	ConvertString(string) (string, error)
// }

// FetchDynamicURLContent is a wrapper for submit & fetch dynamic url content.
// When apiKey is not empty and outputMarkdown is true, it converts the fetched HTML body
// to markdown. If conversion fails, it returns the raw HTML body unchanged.
//
//nolint:gocognit // complex but straightforward state-machine loop
func FetchDynamicURLContent(ctx context.Context, rdb *rlibs.DB, url, apiKey string, outputMarkdown bool) ([]byte, error) {
	logger := gmw.GetLogger(ctx)
	if logger != nil {
		logger = logger.Named("fetch_dynamic_url_content").With(
			zap.String("url", sanitizeURLForLog(url)),
			zap.Bool("output_markdown", outputMarkdown),
		)
		logger.Debug("submitting html crawler task")
	}

	// submit task
	taskID, err := rdb.AddHTMLCrawlerTaskWithOptions(ctx, url, apiKey, outputMarkdown)
	if err != nil {
		if logger != nil {
			logger.Debug("submit html crawler task failed", zap.Error(err))
		}
		return nil, errors.Wrap(err, "submit task")
	}

	if logger != nil {
		logger = logger.With(zap.String("task_id", taskID))
		logger.Debug("submitted html crawler task")
	}

	// fetch task result
	lastStatus := ""
	for {
		task, err := rdb.GetHTMLCrawlerTaskResult(ctx, taskID)
		if err != nil {
			if logger != nil {
				logger.Debug("get html crawler task result failed", zap.Error(err))
			}
			return nil, errors.Wrap(err, "get task result")
		}

		if logger != nil && task.Status != lastStatus {
			logger.Debug("html crawler task status updated",
				zap.String("status", task.Status),
				zap.Bool("task_output_markdown", task.OutputMarkdown),
			)
			lastStatus = task.Status
		}

		switch task.Status {
		case rlibs.TaskStatusSuccess:
			if task.OutputMarkdown && outputMarkdown {
				if logger != nil {
					logger.Debug("html crawler task completed with markdown",
						zap.Int("content_len", len(task.ResultMarkdown)),
					)
				}
				return task.ResultMarkdown, nil
			}

			if logger != nil {
				logger.Debug("html crawler task completed with html",
					zap.Int("content_len", len(task.ResultHTML)),
					zap.Bool("task_output_markdown", task.OutputMarkdown),
				)
			}

			return task.ResultHTML, nil
		case rlibs.TaskStatusPending,
			rlibs.TaskStatusRunning:
			time.Sleep(time.Second)
			continue
		case rlibs.TaskStatusFailed:
			if logger != nil {
				fields := []zap.Field{}
				if task.FailedReason != nil {
					fields = append(fields, zap.String("failed_reason", *task.FailedReason))
				}
				if task.FinishedAt != nil {
					fields = append(fields, zap.Time("finished_at", *task.FinishedAt))
				}
				logger.Debug("html crawler task failed", fields...)
			}
			return nil, errors.Errorf("task failed at %s for reason %q",
				*task.FinishedAt, *task.FailedReason)
		default:
			if logger != nil {
				logger.Debug("html crawler task returned unknown status", zap.String("status", task.Status))
			}
			return nil, errors.Errorf("unknown task status %q", task.Status)
		}
	}
}

// sanitizeURLForLog removes query and fragment components before logging a URL.
// It returns the trimmed original string when parsing fails.
func sanitizeURLForLog(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return strings.TrimSpace(rawURL)
	}

	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}
