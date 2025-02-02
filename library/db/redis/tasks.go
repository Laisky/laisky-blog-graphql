package redis

import (
	"context"
	"time"

	"github.com/Laisky/errors/v2"
	gutils "github.com/Laisky/go-utils/v5"
)

// AddLLMStormTask adds a new LLMStormTask to the queue
func (db *DB) AddLLMStormTask(ctx context.Context,
	prompt string,
	apiKey string,
) (taskID string, err error) {
	taskID = gutils.UUID7()

	task := &LLMStormTask{
		TaskID:    taskID,
		CreatedAt: time.Now(),
		Prompt:    prompt,
		APIKey:    apiKey,
	}

	key := KeyPrefixTaskLLMStorm + task.TaskID
	if err = db.db.RPush(ctx, key, task); err != nil {
		return taskID, errors.Wrap(err, "rpush")
	}

	return taskID, nil
}
