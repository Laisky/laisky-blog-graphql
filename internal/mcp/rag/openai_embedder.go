package rag

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	errors "github.com/Laisky/errors/v2"
	logSDK "github.com/Laisky/go-utils/v6/log"
	"github.com/Laisky/zap"
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
	logger     logSDK.Logger
}

// NewOpenAIEmbedder constructs an embedder for the configured model.
func NewOpenAIEmbedder(baseURL, model string, httpClient *http.Client, opts ...OpenAIEmbedderOption) *OpenAIEmbedder {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	trimmedBaseURL := normalizeBaseURL(baseURL)
	e := &OpenAIEmbedder{
		baseURL:    trimmedBaseURL,
		model:      strings.TrimSpace(model),
		httpClient: httpClient,
	}
	for _, opt := range opts {
		opt(e)
	}
	if e.logger != nil {
		e.logger.Debug("openai embedder initialized",
			zap.String("base_url", e.baseURL),
			zap.String("model", e.model),
			zap.String("embeddings_endpoint", e.baseURL+"/v1/embeddings"),
		)
	}
	return e
}

// OpenAIEmbedderOption configures optional fields on OpenAIEmbedder.
type OpenAIEmbedderOption func(*OpenAIEmbedder)

// WithLogger sets the logger for debug diagnostics.
func WithLogger(logger logSDK.Logger) OpenAIEmbedderOption {
	return func(e *OpenAIEmbedder) {
		e.logger = logger
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

	endpoint := e.baseURL + "/v1/embeddings"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
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
		// Read a limited portion of the response body for diagnostics.
		respSnippet, _ := io.ReadAll(io.LimitReader(httpResp.Body, 512))
		if e.logger != nil {
			e.logger.Debug("embeddings endpoint returned non-2xx",
				zap.String("url", endpoint),
				zap.String("model", e.model),
				zap.Int("status", httpResp.StatusCode),
				zap.Int("batch_size", len(batch)),
				zap.String("response_body", string(respSnippet)),
			)
		}
		return nil, errors.Errorf("embeddings endpoint status %d, url=%s, body=%s",
			httpResp.StatusCode, endpoint, string(respSnippet))
	}

	var decoded embeddingsResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&decoded); err != nil {
		return nil, errors.Wrap(err, "decode embeddings response")
	}

	return &decoded, nil
}

// normalizeBaseURL strips whitespace, trailing slashes, and any OpenAI-style
// path suffixes ("/v1", "/v1/", "/v1/embeddings", "/v1/embeddings/") so the
// caller can unconditionally append "/v1/embeddings".
//
// Examples:
//
//	"https://api.openai.com/v1"             → "https://api.openai.com"
//	"https://api.openai.com/v1/"            → "https://api.openai.com"
//	"https://api.openai.com/v1/embeddings"  → "https://api.openai.com"
//	"https://api.openai.com/v1/embeddings/" → "https://api.openai.com"
//	"https://oneapi.laisky.com"             → "https://oneapi.laisky.com"
//	"https://oneapi.laisky.com/"            → "https://oneapi.laisky.com"
func normalizeBaseURL(raw string) string {
	u := strings.TrimSpace(raw)
	u = strings.TrimRight(u, "/")
	for _, suffix := range []string{"/embeddings", "/v1"} {
		u = strings.TrimSuffix(u, suffix)
		u = strings.TrimRight(u, "/")
	}
	return u
}
