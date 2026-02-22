package rag

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"

	errors "github.com/Laisky/errors/v2"
	pgvector "github.com/pgvector/pgvector-go"
)

// Embedder converts text into vector representations.
type Embedder interface {
	EmbedTexts(ctx context.Context, apiKey string, inputs []string) ([]pgvector.Vector, error)
}

// OpenAIEmbedder calls an OpenAI-compatible embeddings endpoint per request.
type OpenAIEmbedder struct {
	baseURL    string
	model      string
	httpClient *http.Client
}

// NewOpenAIEmbedder constructs an embedder for the configured model.
func NewOpenAIEmbedder(baseURL, model string, httpClient *http.Client) *OpenAIEmbedder {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	trimmedBaseURL := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	return &OpenAIEmbedder{
		baseURL:    trimmedBaseURL,
		model:      strings.TrimSpace(model),
		httpClient: httpClient,
	}
}

// EmbedTexts batches the input strings and returns their vector representations.
func (e *OpenAIEmbedder) EmbedTexts(ctx context.Context, apiKey string, inputs []string) ([]pgvector.Vector, error) {
	if len(inputs) == 0 {
		return nil, errors.New("no inputs provided for embedding")
	}
	if strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("missing api key for embeddings")
	}
	if e == nil {
		return nil, errors.New("embedder is nil")
	}
	if e.httpClient == nil {
		return nil, errors.New("http client is nil")
	}
	if e.baseURL == "" {
		return nil, errors.New("missing embeddings base url")
	}
	if e.model == "" {
		return nil, errors.New("missing embeddings model")
	}

	vectors := make([]pgvector.Vector, 0, len(inputs))
	batchSize := 32
	for start := 0; start < len(inputs); start += batchSize {
		end := start + batchSize
		if end > len(inputs) {
			end = len(inputs)
		}
		batch := inputs[start:end]
		resp, err := e.createEmbeddings(ctx, apiKey, batch)
		if err != nil {
			return nil, errors.Wrap(err, "create embeddings")
		}
		for _, data := range resp.Data {
			values := make([]float32, len(data.Embedding))
			for i, value := range data.Embedding {
				values[i] = float32(value)
			}
			vectors = append(vectors, pgvector.NewVector(values))
		}
	}

	return vectors, nil
}

type embeddingsRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embeddingsResponse struct {
	Data []embeddingsDataItem `json:"data"`
}

type embeddingsDataItem struct {
	Embedding []float64 `json:"embedding"`
}

// createEmbeddings sends one embeddings batch request and parses vectors from response.
func (e *OpenAIEmbedder) createEmbeddings(ctx context.Context, apiKey string, batch []string) (*embeddingsResponse, error) {
	body, err := json.Marshal(embeddingsRequest{Model: e.model, Input: batch})
	if err != nil {
		return nil, errors.Wrap(err, "marshal embeddings request")
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/v1/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, errors.Wrap(err, "build embeddings request")
	}
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := e.httpClient.Do(httpReq)
	if err != nil {
		return nil, errors.Wrap(err, "call embeddings endpoint")
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode < http.StatusOK || httpResp.StatusCode >= http.StatusMultipleChoices {
		return nil, errors.Errorf("embeddings endpoint status %d", httpResp.StatusCode)
	}

	var decoded embeddingsResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&decoded); err != nil {
		return nil, errors.Wrap(err, "decode embeddings response")
	}

	return &decoded, nil
}
