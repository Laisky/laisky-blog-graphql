package redis

import "time"

// LLMStormTask represents a task for LLM storm
type LLMStormTask struct {
	TaskID    string    `json:"task_id"`
	Prompt    string    `json:"prompt"`
	APIKey    string    `json:"api_key"`
	CreatedAt time.Time `json:"created_at"`
}
