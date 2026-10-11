package redis

import (
	"context"
	"strings"

	"github.com/Laisky/errors/v2"
	"github.com/redis/go-redis/v9"

	"github.com/Laisky/laisky-blog-graphql/library/crawleregress"
)

// AddLLMStormTask adds a new LLMStormTask to the queue.
func (db *DB) AddLLMStormTask(ctx context.Context,
	prompt string,
	apiKey string,
) (taskID string, err error) {
	task := NewLLMStormTask(prompt, apiKey)
	payload, err := task.ToString()
	if err != nil {
		return "", errors.Wrap(err, "failed to serialize task using ToString")
	}

	if err = db.db.RPush(ctx, KeyTaskLLMStormPending, []any{payload}); err != nil {
		return "", errors.Wrapf(err, "failed to push task to key `%s`", KeyTaskLLMStormPending)
	}

	return task.TaskID, nil
}

// GetLLMStormTaskResult gets the result of a LLMStormTask by taskID.
func (db *DB) GetLLMStormTaskResult(ctx context.Context, taskID string) (task *LLMStormTask, err error) {
	key := KeyPrefixTaskLLMStormResult + taskID
	val, err := db.db.GetItem(ctx, key)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get task result by key `%s`", key)
	}

	// Check if the returned string is empty or only whitespace.
	if strings.TrimSpace(val) == "" {
		return nil, errors.Errorf("empty task result for taskID `%s` at key `%s`", taskID, key)
	}

	task, err = NewLLMStormTaskFromString(val)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to parse task result from string for taskID `%s`", taskID)
	}

	return task, nil
}

// AddHTMLCrawlerTask adds a new HTMLCrawlerTask to the queue.
func (db *DB) AddHTMLCrawlerTask(ctx context.Context, url string) (taskID string, err error) {
	return db.AddHTMLCrawlerTaskWithOptions(ctx, url, "", true)
}

// AddHTMLCrawlerTaskWithOptions adds a new HTMLCrawlerTask to the queue with extra fields.
// It admits a public target and pins the default connection policy before enqueue.
func (db *DB) AddHTMLCrawlerTaskWithOptions(ctx context.Context, url, apiKey string, outputMarkdown bool) (taskID string, err error) {
	return db.AddHTMLCrawlerTaskWithEgress(ctx, url, apiKey, outputMarkdown, nil)
}

// AddHTMLCrawlerTaskWithEgress queues a crawl together with the connection
// policy request admission produced for it. The retained apiKey argument is never
// queued because local rendering and markdown conversion need no provider credentials.
func (db *DB) AddHTMLCrawlerTaskWithEgress(ctx context.Context, url, apiKey string,
	outputMarkdown bool, egress *CrawlerEgressPolicy,
) (taskID string, err error) {
	if egress == nil {
		admission, admitErr := crawleregress.AdmitURL(ctx, url)
		if admitErr != nil {
			return "", errors.Wrap(admitErr, "admit crawler target")
		}
		policy := admission.Policy(crawleregress.DefaultMaxRedirects, false)
		egress = &policy
	}
	if _, policyErr := crawleregress.ValidatePolicy(ctx, url, *egress); policyErr != nil {
		return "", errors.Wrap(policyErr, "validate crawler task policy")
	}
	task := NewHTMLCrawlerTaskWithEgress(url, "", outputMarkdown, egress)
	payload, err := task.ToString()
	if err != nil {
		return "", errors.Wrap(err, "failed to serialize task using ToString")
	}

	if err = db.db.RPush(ctx, KeyTaskHTMLCrawlerPending, []any{payload}); err != nil {
		return "", errors.Wrapf(err, "failed to push task to key `%s`", KeyTaskHTMLCrawlerPending)
	}

	return task.TaskID, nil
}

// GetHTMLCrawlerTask is to get a HTMLCrawlerTask from the queue.
func (db *DB) GetHTMLCrawlerTask(ctx context.Context) (task *HTMLCrawlerTask, err error) {
	_, val, err := db.db.LPopKeysBlocking(ctx, KeyTaskHTMLCrawlerPending)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to pop task from key `%s`", KeyTaskHTMLCrawlerPending)
	}

	if val == "OK" {
		return nil, errors.Wrapf(redis.Nil, "got 'OK' for key %q", KeyTaskHTMLCrawlerPending)
	}

	task, err = NewHTMLCrawlerTaskFromString(val)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to parse html crawler task from string")
	}

	return task, nil
}

// GetHTMLCrawlerTaskResult gets the result of a HTMLCrawlerTask by taskID.
func (db *DB) GetHTMLCrawlerTaskResult(ctx context.Context, taskID string) (task *HTMLCrawlerTask, err error) {
	key := KeyPrefixTaskHTMLCrawlerResult + taskID
	val, err := db.db.GetItemBlocking(ctx, key)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get task result by key `%s`", key)
	}

	// Check if the returned string is empty or only whitespace.
	if strings.TrimSpace(val) == "" {
		return nil, errors.Errorf("empty task result for taskID `%s` at key `%s`", taskID, key)
	}

	task, err = NewHTMLCrawlerTaskFromString(val)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to parse task result from string for taskID `%s`", taskID)
	}

	return task, nil
}
