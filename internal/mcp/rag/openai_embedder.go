package rag

import (
	"context"
	"fmt"
	"net/http"

	pgvector "github.com/pgvector/pgvector-go"
	openai "github.com/sashabaranov/go-openai"
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
	return &OpenAIEmbedder{
		baseURL:    baseURL,
		model:      model,
		httpClient: httpClient,
	}
}

// EmbedTexts batches the input strings and returns their vector representations.
func (e *OpenAIEmbedder) EmbedTexts(ctx context.Context, apiKey string, inputs []string) ([]pgvector.Vector, error) {
	if len(inputs) == 0 {
		return nil, fmt.Errorf("no inputs provided for embedding")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("missing api key for embeddings")
	}

	cfg := openai.DefaultConfig(apiKey)
	if e.baseURL != "" {
		cfg.BaseURL = e.baseURL
	}
	cfg.HTTPClient = e.httpClient
	client := openai.NewClientWithConfig(cfg)

	vectors := make([]pgvector.Vector, 0, len(inputs))
	batchSize := 32
	for start := 0; start < len(inputs); start += batchSize {
		end := start + batchSize
		if end > len(inputs) {
			end = len(inputs)
		}
		batch := inputs[start:end]
		resp, err := client.CreateEmbeddings(ctx, openai.EmbeddingRequest{
			Model: openai.EmbeddingModel(e.model),
			Input: batch,
		})
		if err != nil {
			return nil, fmt.Errorf("create embeddings: %w", err)
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
