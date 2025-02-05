package redis

import (
	"context"

	"github.com/Laisky/errors/v2"
)

// AddLLMStormTask adds a new LLMStormTask to the queue
func (db *DB) AddLLMStormTask(ctx context.Context,
	prompt string,
	apiKey string,
) (taskID string, err error) {
	task := NewLLMStormTask(prompt, apiKey)
	payload, err := task.ToString()
	if err != nil {
		return taskID, errors.Wrap(err, "task.ToString")
	}

	if err = db.db.RPush(ctx, KeyTaskLLMStormPending, payload); err != nil {
		return taskID, errors.Wrapf(err, "push task to key `%s`", KeyTaskLLMStormPending)
	}

	return taskID, nil
}

// GetLLMStormTaskResult gets the result of a LLMStormTask by taskID
func (db *DB) GetLLMStormTaskResult(ctx context.Context, taskID string) (task *LLMStormTask, err error) {
	key := KeyPrefixTaskLLMStormResult + taskID
	_, val, err := db.db.LPopKeysBlocking(ctx, key)
	if err != nil {
		return nil, errors.Wrapf(err, "get task result by key `%s`", key)
	}

	task, err = NewLLMStormTaskFromString(val)
	if err != nil {
		return nil, errors.Wrapf(err, "parse task `%s` result from string", taskID)
	}

	return task, nil
}
