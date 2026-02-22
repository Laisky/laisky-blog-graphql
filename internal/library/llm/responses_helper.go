package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	errors "github.com/Laisky/errors/v2"
)

const (
	defaultAPIBase = "https://oneapi.laisky.com"
)

// ResponsesHelper wraps OpenAI-compatible Responses API calls.
type ResponsesHelper struct {
	apiBase    string
	httpClient *http.Client
}

// ResponseRequest describes one Responses API generation request.
type ResponseRequest struct {
	Model           string
	Instructions    string
	Input           string
	PromptCacheKey  string
	MaxOutputTokens int
	Temperature     float64
}

// NewResponsesHelper creates a Responses API helper with safe defaults.
func NewResponsesHelper(apiBase string, timeout time.Duration, httpClient *http.Client) *ResponsesHelper {
	trimmedBase := strings.TrimSpace(apiBase)
	if trimmedBase == "" {
		trimmedBase = defaultAPIBase
	}
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: timeout}
	}

	return &ResponsesHelper{
		apiBase:    strings.TrimRight(trimmedBase, "/"),
		httpClient: httpClient,
	}
}

// CreateText sends a Responses API request and returns aggregated text output.
func (h *ResponsesHelper) CreateText(ctx context.Context, apiKey string, req ResponseRequest) (string, error) {
	if h == nil {
		return "", errors.New("responses helper is nil")
	}
	if strings.TrimSpace(apiKey) == "" {
		return "", errors.New("missing api key")
	}
	if strings.TrimSpace(req.Model) == "" {
		return "", errors.New("missing model")
	}
	if strings.TrimSpace(req.Input) == "" {
		return "", errors.New("missing input")
	}

	payload := map[string]any{
		"model": req.Model,
		"input": req.Input,
	}
	if strings.TrimSpace(req.Instructions) != "" {
		payload["instructions"] = req.Instructions
	}
	if strings.TrimSpace(req.PromptCacheKey) != "" {
		payload["prompt_cache_key"] = req.PromptCacheKey
	}
	if req.MaxOutputTokens > 0 {
		payload["max_output_tokens"] = req.MaxOutputTokens
	}
	if req.Temperature >= 0 {
		payload["temperature"] = req.Temperature
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", errors.Wrap(err, "marshal responses request")
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, h.apiBase+"/v1/responses", bytes.NewReader(body))
	if err != nil {
		return "", errors.Wrap(err, "build responses request")
	}
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := h.httpClient.Do(httpReq)
	if err != nil {
		return "", errors.Wrap(err, "call responses endpoint")
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", errors.Errorf("responses endpoint status %d", resp.StatusCode)
	}

	var decoded responsesCreateResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return "", errors.Wrap(err, "decode responses response")
	}

	text := strings.TrimSpace(decoded.OutputText)
	if text != "" {
		return text, nil
	}

	text = strings.TrimSpace(decoded.AggregatedText())
	if text == "" {
		return "", errors.New("responses output text is empty")
	}

	return text, nil
}

type responsesCreateResponse struct {
	OutputText string                 `json:"output_text"`
	Output     []responsesOutputItem  `json:"output"`
}

func (r responsesCreateResponse) AggregatedText() string {
	parts := make([]string, 0, len(r.Output))
	for _, item := range r.Output {
		for _, content := range item.Content {
			if strings.EqualFold(content.Type, "output_text") || strings.EqualFold(content.Type, "text") {
				if text := strings.TrimSpace(content.Text); text != "" {
					parts = append(parts, text)
				}
			}
		}
	}

	return strings.Join(parts, "\n")
}

type responsesOutputItem struct {
	Type    string                   `json:"type"`
	Content []responsesOutputContent `json:"content"`
}

type responsesOutputContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}
