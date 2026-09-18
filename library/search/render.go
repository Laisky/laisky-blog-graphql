package search

import (
	"context"
	"time"

	"github.com/Laisky/errors/v2"
	gmw "github.com/Laisky/gin-middlewares/v7"
	"github.com/Laisky/zap"

	"github.com/Laisky/laisky-blog-graphql/internal/library/toolpolicy"
	rlibs "github.com/Laisky/laisky-blog-graphql/library/db/redis"
)

// type htmlToMarkdownConverter interface {
// 	ConvertString(string) (string, error)
// }

// FetchDynamicURLContent is a wrapper for submit & fetch dynamic url content.
// When apiKey is not empty and outputMarkdown is true, it converts the fetched HTML body
// to markdown. If conversion fails, it returns the raw HTML body unchanged.
//
// The crawl carries the connection policy request admission produced, and the
// origins the renderer reports are re-admitted before any body is returned, so
// a redirect escape or a rebound host fails closed after the fact.
//
//nolint:gocognit // complex but straightforward state-machine loop
func FetchDynamicURLContent(ctx context.Context, rdb *rlibs.DB, url, apiKey string, outputMarkdown bool) ([]byte, error) {
	// gmw.GetLogger always returns a usable logger, so no nil guard is needed.
	logger := gmw.GetLogger(ctx).Named("fetch_dynamic_url_content").With(
		zap.String("url", sanitizeURLForLog(url)),
		zap.Bool("output_markdown", outputMarkdown),
	)
	logger.Debug("submitting html crawler task")

	// Re-run admission here so the pinned addresses belong to this crawl, not
	// to whatever an earlier caller validated. This is the same policy the
	// entry points already applied, so an admitted URL cannot be rejected now.
	admission, err := toolpolicy.AdmitFetchURL(ctx, url)
	if err != nil {
		logger.Debug("fetch target is not admissible", zap.Error(err))
		return nil, errors.Wrap(err, "admit fetch target")
	}
	egressSettings := LoadEgressSettings()
	policy := admission.Policy(egressSettings.MaxRedirects, egressSettings.AllowSubresources)

	// submit task
	taskID, err := rdb.AddHTMLCrawlerTaskWithEgress(ctx, url, apiKey, outputMarkdown, crawlerEgressPolicy(policy))
	if err != nil {
		logger.Debug("submit html crawler task failed", zap.Error(err))
		return nil, errors.Wrap(err, "submit task")
	}

	logger = logger.With(zap.String("task_id", taskID))
	logger.Debug("submitted html crawler task")

	// fetch task result
	lastStatus := ""
	for {
		task, err := rdb.GetHTMLCrawlerTaskResult(ctx, taskID)
		if err != nil {
			logger.Debug("get html crawler task result failed", zap.Error(err))
			return nil, errors.Wrap(err, "get task result")
		}

		if task.Status != lastStatus {
			logger.Debug("html crawler task status updated",
				zap.String("status", task.Status),
				zap.Bool("task_output_markdown", task.OutputMarkdown),
			)
			lastStatus = task.Status
		}

		switch task.Status {
		case rlibs.TaskStatusSuccess:
			// Nothing is returned to the caller before the reported chain is
			// re-admitted, so a renderer that escaped the policy cannot
			// exfiltrate a private response body.
			if egressErr := verifyRenderedEgress(ctx, logger, egressSettings, policy, task.RequestChain); egressErr != nil {
				logger.Error("rejecting crawler result", zap.Error(egressErr))
				return nil, egressErr
			}
			if task.OutputMarkdown && outputMarkdown {
				logger.Debug("html crawler task completed with markdown",
					zap.Int("content_len", len(task.ResultMarkdown)),
				)
				return task.ResultMarkdown, nil
			}

			logger.Debug("html crawler task completed with html",
				zap.Int("content_len", len(task.ResultHTML)),
				zap.Bool("task_output_markdown", task.OutputMarkdown),
			)

			return task.ResultHTML, nil
		case rlibs.TaskStatusPending,
			rlibs.TaskStatusRunning:
			select {
			case <-ctx.Done():
				return nil, errors.Wrap(ctx.Err(), "wait for crawler result")
			case <-time.After(time.Second):
			}
			continue
		case rlibs.TaskStatusFailed:
			fields := []zap.Field{}
			if task.FailedReason != nil {
				fields = append(fields, zap.String("failed_reason", *task.FailedReason))
			}
			if task.FinishedAt != nil {
				fields = append(fields, zap.Time("finished_at", *task.FinishedAt))
			}
			logger.Debug("html crawler task failed", fields...)
			return nil, errors.New("crawler task failed; inspect the task audit record")
		default:
			logger.Debug("html crawler task returned unknown status", zap.String("status", task.Status))
			return nil, errors.Errorf("unknown task status %q", task.Status)
		}
	}
}

// sanitizeURLForLog removes query and fragment components before logging a URL.
// It redacts malformed inputs instead of logging their original bytes.
func sanitizeURLForLog(rawURL string) string {
	return toolpolicy.URLForLog(rawURL)
}
