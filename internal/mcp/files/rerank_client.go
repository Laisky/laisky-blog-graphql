package files

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	errors "github.com/Laisky/errors/v2"
)

// CohereRerankClient calls a Cohere-compatible rerank endpoint.
type CohereRerankClient struct {
	endpoint string
	model    string
	client   *http.Client
}

// NewCohereRerankClient constructs a rerank client with the configured endpoint.
func NewCohereRerankClient(endpoint, model string, timeout time.Duration) *CohereRerankClient {
	if timeout <= 0 {
		timeout = 6 * time.Second
	}
	return &CohereRerankClient{
		endpoint: endpoint,
		model:    model,
		client:   &http.Client{Timeout: timeout},
	}
}

// Rerank sends documents to the external rerank service.
func (c *CohereRerankClient) Rerank(ctx context.Context, apiKey, query string, docs []string) ([]float64, error) {
	if c == nil {
		return nil, errors.New("rerank client is nil")
	}
	if apiKey == "" {
		return nil, errors.New("missing api key")
	}
	payload := map[string]any{
		"model":     c.model,
		"query":     query,
		"documents": docs,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, errors.Wrap(err, "marshal rerank payload")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, errors.Wrap(err, "build rerank request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, errors.Wrap(err, "call rerank endpoint")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(resp.Body)
		return nil, errors.Errorf("rerank endpoint status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}

	var parsed struct {
		Results []struct {
			Index int     `json:"index"`
			Score float64 `json:"relevance_score"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, errors.Wrap(err, "decode rerank response")
	}

	scores := make([]float64, len(docs))
	for _, result := range parsed.Results {
		if result.Index >= 0 && result.Index < len(scores) {
			scores[result.Index] = result.Score
		}
	}
	return scores, nil
}
