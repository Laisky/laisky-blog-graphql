package search

import (
	"github.com/Laisky/errors/v2"

	"github.com/Laisky/laisky-blog-graphql/internal/library/toolpolicy"
	"github.com/Laisky/laisky-blog-graphql/library/crawleregress"
	rlibs "github.com/Laisky/laisky-blog-graphql/library/db/redis"
)

func failedCrawlerTask(taskID, target string, policy toolpolicy.EgressPolicy, markdown bool, task *rlibs.HTMLCrawlerTask) error {
	diagnostic := FetchFailureDiagnostic{ErrorCode: failureCodeCrawler, Stage: failureStageRender, TaskID: safeTaskID(taskID)}
	// Failure metadata is attributed only to the exact submitted operation. A
	// receipt is unnecessary for telemetry because this path never releases a body.
	if diagnostic.TaskID != "" && task != nil && task.Status == rlibs.TaskStatusFailed && task.TaskID == taskID && task.Url == target &&
		task.OutputMarkdown == markdown && task.Egress != nil &&
		crawleregress.PolicyDigest(target, *task.Egress) == crawleregress.PolicyDigest(target, policy) {
		diagnostic.WorkerStage, diagnostic.WorkerCategory = crawleregress.WorkerFailureDiagnostic(task.FailureStage, task.FailureCategory)
	}
	// Legacy FailedReason is deliberately not parsed or logged.
	return errors.WithStack(&FetchFailureError{Diagnostic: diagnostic, Cause: errors.New("crawler task failed")})
}
