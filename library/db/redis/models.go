package redis

import (
	"time"

	gutils "github.com/Laisky/go-utils/v6"
	"github.com/Laisky/go-utils/v6/json"
	"github.com/pkg/errors"
)

const (
	TaskStatusPending = "pending"
	TaskStatusRunning = "running"
	TaskStatusSuccess = "success"
	TaskStatusFailed  = "failed"
)

type baseTask struct {
	TaskID       string     `json:"task_id"`
	CreatedAt    time.Time  `json:"created_at"`
	Status       string     `json:"status"`
	FailedReason *string    `json:"failed_reason,omitempty"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
}

func newBaseTask() baseTask {
	return baseTask{
		TaskID:    gutils.UUID7(),
		CreatedAt: time.Now(),
		Status:    TaskStatusPending,
	}
}

type LLMStormTask struct {
	baseTask
	Prompt           string           `json:"prompt"`
	APIKey           string           `json:"api_key"`
	ResultArticle    *string          `json:"result_article,omitempty"`
	ResultReferences *stormReferences `json:"result_references,omitempty"`
	// Runner is the name of the runner that processed the task
	Runner string `json:"runner"`
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
		return nil, errors.Wrapf(err, "unmarshal llm storm task %q", taskStr)
	}
	return &task, nil
}

// NewLLMStormTask creates a new StormTask instance.
func NewLLMStormTask(prompt, apikey string) *LLMStormTask {
	return &LLMStormTask{
		baseTask: newBaseTask(),
		Prompt:   prompt,
		APIKey:   apikey,
	}
}

// HTMLCrawlerTask is a task for crawling HTML pages.
type HTMLCrawlerTask struct {
	baseTask
	// Url is the target URL to fetch
	Url string `json:"url"`
	// APIKey is the one-api api key used for fetching dynamic content
	APIKey string `json:"api_key,omitempty"`
	// OutputMarkdown indicates whether the fetched HTML should be converted to markdown
	OutputMarkdown bool `json:"output_markdown,omitempty"`
	// ResultHTML is the raw fetched HTML body, always present no matter OutputMarkdown is true or false
	ResultHTML []byte `json:"result_html,omitempty"`
	// ResultMarkdown if OutputMarkdown is true, the fetched HTML body converted to markdown
	ResultMarkdown []byte `json:"result_markdown,omitempty"`
}

// ToString returns the JSON representation of a HTMLCrawlerTask.
func (s *HTMLCrawlerTask) ToString() (string, error) {
	data, err := json.MarshalToString(s)
	if err != nil {
		return "", errors.Wrap(err, "marshal")
	}

	return data, nil
}

// NewHTMLCrawlerTaskFromString creates a HTMLCrawlerTask instance from its JSON string representation.
func NewHTMLCrawlerTaskFromString(taskStr string) (*HTMLCrawlerTask, error) {
	var task HTMLCrawlerTask
	if err := json.Unmarshal([]byte(taskStr), &task); err != nil {
		return nil, errors.Wrapf(err, "unmarshal html crawler task %q", taskStr)
	}

	return &task, nil
}

// NewHTMLCrawlerTask creates a new HTMLCrawlerTask instance.

// NewHTMLCrawlerTask creates a new HTMLCrawlerTask instance.
func NewHTMLCrawlerTask(url string) *HTMLCrawlerTask {
	return NewHTMLCrawlerTaskWithOptions(url, "", false)
}

// NewHTMLCrawlerTaskWithOptions creates a new HTMLCrawlerTask instance with additional options.
// apiKey may be empty. When outputMarkdown is true and apiKey is not empty, the task runner may
// convert the fetched HTML body to markdown.
func NewHTMLCrawlerTaskWithOptions(url, apiKey string, outputMarkdown bool) *HTMLCrawlerTask {
	return &HTMLCrawlerTask{
		baseTask:       newBaseTask(),
		Url:            url,
		APIKey:         apiKey,
		OutputMarkdown: outputMarkdown,
	}
}
