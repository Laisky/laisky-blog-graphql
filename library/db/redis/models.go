package redis

import (
	"time"

	gutils "github.com/Laisky/go-utils/v5"
	"github.com/Laisky/go-utils/v5/json"
	"github.com/pkg/errors"
)

const (
	TaskStatusPending = "pending"
	TaskStatusRunning = "running"
	TaskStatusSuccess = "success"
	TaskStatusFailed  = "failed"
)

type LLMStormTask struct {
	TaskID           string           `json:"task_id"`
	Prompt           string           `json:"prompt"`
	APIKey           string           `json:"api_key"`
	CreatedAt        time.Time        `json:"created_at"`
	Status           string           `json:"status"`
	FailedReason     *string          `json:"failed_reason,omitempty"`
	FinishedAt       *time.Time       `json:"finished_at,omitempty"`
	ResultArticle    *string          `json:"result_article,omitempty"`
	ResultReferences *stormReferences `json:"result_references,omitempty"`
}

type stormReferences struct {
	UrlToUnifiedIndex map[string]int          `json:"url_to_unified_index"`
	UrlToInfo         map[string]stormUrlInfo `json:"url_to_info"`
}

type stormUrlInfo struct {
	Url          string           `json:"url"`
	Description  string           `json:"description"`
	Snippets     []string         `json:"snippets"`
	Title        string           `json:"title"`
	Meta         stormUrlInfoMeta `json:"meta"`
	CitationUUID int              `json:"citation_uuid"`
}

type stormUrlInfoMeta struct {
	Query string `json:"query"`
}

// ToString returns the JSON representation of a StormTask.
func (s *LLMStormTask) ToString() (string, error) {
	data, err := json.MarshalToString(s)
	if err != nil {
		return "", errors.Wrap(err, "marshal")
	}

	return data, nil
}

// NewLLMStormTaskFromString creates a StormTask instance from its JSON string representation.
func NewLLMStormTaskFromString(taskStr string) (*LLMStormTask, error) {
	var task LLMStormTask
	if err := json.Unmarshal([]byte(taskStr), &task); err != nil {
		return nil, errors.Wrap(err, "unmarshal")
	}
	return &task, nil
}

// NewLLMStormTask creates a new StormTask instance.
func NewLLMStormTask(prompt, apikey string) *LLMStormTask {
	return &LLMStormTask{
		TaskID:    gutils.UUID7(),
		Prompt:    prompt,
		APIKey:    apikey,
		CreatedAt: time.Now(),
		Status:    TaskStatusPending,
	}
}
