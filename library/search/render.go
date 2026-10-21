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
// Direct Markdown conversion is local on the renderer. apiKey is retained for caller compatibility
// and billing admission; it is never placed in the crawler task or sent downstream.
//
// The crawl carries the connection policy request admission produced, and the
// acquisition is checked before any body is returned. Direct results require
// pinned connection receipts; configured provider results are identified as such.
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
	start := time.Now()
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
	// AddHTMLCrawlerTaskWithEgress advertises this capability in the queued task.
	// Keep the expected value here, independently of the returned task envelope.
	const allowProviderFallback = true

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
			logger.Info("html crawler result observed",
				zap.String("event", "result_observed"), zap.String("stage", "result"),
				zap.String("status", safeCrawlerStatus(task.Status)),
				zap.String("caller_context_state", FetchCallerContextState(ctx)),
				zap.Int64("duration_ms", min(max(time.Since(start).Milliseconds(), 0), 24*60*60*1000)),
			)
			lastStatus = task.Status
		}

		switch task.Status {
		case rlibs.TaskStatusSuccess:
			// No body is returned until identity, policy and acquisition checks pass.
			if egressErr := verifyAcquiredTask(ctx, taskID, url, policy, allowProviderFallback, task); egressErr != nil {
				return nil, egressErr
			}
			if task.OutputMarkdown != outputMarkdown || len(task.ResultHTML) > crawleregress.MaxResponseBytes || len(task.ResultMarkdown) > crawleregress.MaxResponseBytes {
				return nil, fetchFailure(crawleregress.ErrRejected, "crawler_egress_rejected", "verification", taskID)
			}
			if (outputMarkdown && len(task.ResultMarkdown) == 0) || (!outputMarkdown && len(task.ResultHTML) == 0) {
				return nil, fetchFailure(errors.New("empty crawler result body"), "crawler_result_failed", "result", taskID)
			}
			// A provider result is accepted under the explicit capability, without
			// claiming a verified direct connection to the target.
			message, event, method, provider := "html crawler result verified", "result_verified", crawleregress.AcquisitionDirect, ""
			if task.Acquisition != nil && task.Acquisition.Method == crawleregress.AcquisitionProvider {
				message, event, method, provider = "html crawler result accepted", "result_accepted", crawleregress.AcquisitionProvider, task.Acquisition.Provider
			}
			logger.Info(message, zap.String("event", event),
				zap.String("stage", "verification"), zap.String("status", rlibs.TaskStatusSuccess),
				zap.String("acquisition_method", method), zap.String("provider", provider),
				zap.String("caller_context_state", FetchCallerContextState(ctx)),
				zap.Int64("duration_ms", min(max(time.Since(start).Milliseconds(), 0), 24*60*60*1000)))
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

func safeCrawlerStatus(status string) string {
	switch status {
	case rlibs.TaskStatusPending, rlibs.TaskStatusRunning, rlibs.TaskStatusSuccess, rlibs.TaskStatusFailed:
		return status
	default:
		return "unknown"
	}
}
