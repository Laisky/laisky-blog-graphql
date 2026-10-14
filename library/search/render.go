package search

import (
	"context"
	"time"

	"github.com/Laisky/errors/v2"
	"github.com/Laisky/zap"

	"github.com/Laisky/laisky-blog-graphql/internal/library/toolpolicy"
	"github.com/Laisky/laisky-blog-graphql/library/crawleregress"
	rlibs "github.com/Laisky/laisky-blog-graphql/library/db/redis"
	appLog "github.com/Laisky/laisky-blog-graphql/library/log"
)

// type htmlToMarkdownConverter interface {
// 	ConvertString(string) (string, error)
// }

// FetchDynamicURLContent is a wrapper for submit & fetch dynamic url content.
// Markdown conversion is local on the renderer. apiKey is retained for caller compatibility
// and billing admission; it is never placed in the crawler task or sent downstream.
//
// The crawl carries the connection policy request admission produced, and the
// bound connection receipts are checked before any body is returned. The renderer
// enforces pinned connections before fetching; verification never re-resolves DNS.
func FetchDynamicURLContent(ctx context.Context, rdb *rlibs.DB, url, apiKey string, outputMarkdown bool) ([]byte, error) {
	return fetchDynamicURLContent(ctx, rdb, url, apiKey, outputMarkdown)
}

// crawlerTaskStore allows offline queue/result behavior tests without real credentials.
type crawlerTaskStore interface {
	AddHTMLCrawlerTaskWithEgress(context.Context, string, string, bool, *rlibs.CrawlerEgressPolicy) (string, error)
	GetHTMLCrawlerTaskResult(context.Context, string) (*rlibs.HTMLCrawlerTask, error)
}

// fetchDynamicURLContent executes the shared submit/result state machine.
//
//nolint:gocognit // complex but straightforward state-machine loop
func fetchDynamicURLContent(ctx context.Context, rdb crawlerTaskStore, url, apiKey string, outputMarkdown bool) ([]byte, error) {
	// Only the application logger carries reviewed operation correlation.
	logger := reviewedFetchLogger(ctx, appLog.Logger).Named("fetch_dynamic_url_content").With(
		zap.Bool("output_markdown", outputMarkdown),
	)
	logger.Debug("submitting html crawler task")

	// Re-run admission here so the pinned addresses belong to this crawl, not
	// to whatever an earlier caller validated. This is the same policy the
	// entry points already applied, so an admitted URL cannot be rejected now.
	admission, err := toolpolicy.AdmitFetchURL(ctx, url)
	if err != nil {
		logger.Debug("fetch target is not admissible")
		return nil, fetchFailure(err, "fetch_admission_failed", "admission", "")
	}
	egressSettings := LoadEgressSettings()
	policy := admission.Policy(egressSettings.MaxRedirects, egressSettings.AllowSubresources)

	// submit task
	taskID, err := rdb.AddHTMLCrawlerTaskWithEgress(ctx, url, "", outputMarkdown, crawlerEgressPolicy(policy))
	if err != nil {
		logger.Debug("submit html crawler task failed")
		return nil, fetchFailure(err, "crawler_submit_failed", "submit", "")
	}

	logger = logger.With(zap.String("task_id", safeTaskID(taskID)))
	logger.Debug("submitted html crawler task")

	// fetch task result
	lastStatus := ""
	for {
		task, err := rdb.GetHTMLCrawlerTaskResult(ctx, taskID)
		if err != nil {
			logger.Debug("get html crawler task result failed")
			return nil, fetchFailure(err, "crawler_result_failed", "result", taskID)
		}

		if task == nil {
			return nil, fetchFailure(errors.New("nil crawler result"), "crawler_result_failed", "result", taskID)
		}
		if task.Status != lastStatus {
			logger.Debug("html crawler task status updated",
				zap.String("status", safeCrawlerStatus(task.Status)),
				zap.Bool("task_output_markdown", task.OutputMarkdown),
			)
			lastStatus = task.Status
		}

		switch task.Status {
		case rlibs.TaskStatusSuccess:
			// No body is returned until the submitted identity and pinned-peer
			// receipt have passed verification.
			if egressErr := verifyRenderedTask(ctx, egressSettings, taskID, url, policy, task); egressErr != nil {
				return nil, egressErr
			}
			if task.OutputMarkdown != outputMarkdown || len(task.ResultHTML) > crawleregress.MaxResponseBytes || len(task.ResultMarkdown) > crawleregress.MaxResponseBytes {
				return nil, fetchFailure(crawleregress.ErrRejected, "crawler_egress_rejected", "verification", taskID)
			}
			if (outputMarkdown && len(task.ResultMarkdown) == 0) || (!outputMarkdown && len(task.ResultHTML) == 0) {
				return nil, fetchFailure(errors.New("empty crawler result body"), "crawler_result_failed", "result", taskID)
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
				return nil, fetchFailure(ctx.Err(), "crawler_result_failed", "wait", taskID)
			case <-time.After(time.Second):
			}
			continue
		case rlibs.TaskStatusFailed:
			return nil, failedCrawlerTask(taskID, url, policy, outputMarkdown, task)
		default:
			logger.Debug("html crawler task returned unknown status", zap.String("status", safeCrawlerStatus(task.Status)))
			return nil, fetchFailure(errors.New("unknown crawler task status"), "crawler_status_invalid", "result", taskID)
		}
	}
}

// sanitizeURLForLog removes query and fragment components before logging a URL.
// It redacts malformed inputs instead of logging their original bytes.
func sanitizeURLForLog(rawURL string) string {
	return toolpolicy.URLForLog(rawURL)
}

func safeCrawlerStatus(status string) string {
	switch status {
	case rlibs.TaskStatusPending, rlibs.TaskStatusRunning, rlibs.TaskStatusSuccess, rlibs.TaskStatusFailed:
		return status
	default:
		return "unknown"
	}
}
