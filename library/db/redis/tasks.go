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
	if err = db.db.RPush(ctx, KeyTaskLLMStormPending, task); err != nil {
		return taskID, errors.Wrap(err, "rpush")
	}

	return taskID, nil
}
