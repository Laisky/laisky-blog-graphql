package rag

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCreateEmbeddings_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/embeddings", r.URL.Path)
		require.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
		require.Equal(t, "application/json", r.Header.Get("Content-Type"))

		resp := embeddingsResponse{
			Data: []embeddingsDataItem{
				{Embedding: []float64{0.1, 0.2, 0.3}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	e := NewOpenAIEmbedder(srv.URL, "test-model", srv.Client())
	vecs, err := e.EmbedTexts(context.Background(), "test-key", []string{"hello"})
	require.NoError(t, err)
	require.Len(t, vecs, 1)
	require.Len(t, vecs[0].Slice(), 3)
}

func TestCreateEmbeddings_404_IncludesResponseBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"endpoint not found"}`))
	}))
	defer srv.Close()

	e := NewOpenAIEmbedder(srv.URL, "test-model", srv.Client())
	_, err := e.EmbedTexts(context.Background(), "test-key", []string{"hello"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "404")
	require.Contains(t, err.Error(), "endpoint not found")
	require.Contains(t, err.Error(), srv.URL+"/v1/embeddings")
}

func TestCreateEmbeddings_500_IncludesResponseBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`internal server error`))
	}))
	defer srv.Close()

	e := NewOpenAIEmbedder(srv.URL, "test-model", srv.Client())
	_, err := e.EmbedTexts(context.Background(), "test-key", []string{"hello"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "500")
	require.Contains(t, err.Error(), "internal server error")
}

func TestCreateEmbeddings_EmptyInputs(t *testing.T) {
	e := NewOpenAIEmbedder("http://localhost", "test-model", nil)
	_, err := e.EmbedTexts(context.Background(), "test-key", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no inputs")
}

func TestCreateEmbeddings_MissingAPIKey(t *testing.T) {
	e := NewOpenAIEmbedder("http://localhost", "test-model", nil)
	_, err := e.EmbedTexts(context.Background(), "", []string{"hello"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing api key")
}

func TestCreateEmbeddings_MissingBaseURL(t *testing.T) {
	e := NewOpenAIEmbedder("", "test-model", nil)
	_, err := e.EmbedTexts(context.Background(), "test-key", []string{"hello"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing embeddings base url")
}

func TestCreateEmbeddings_MissingModel(t *testing.T) {
	e := NewOpenAIEmbedder("http://localhost", "", nil)
	_, err := e.EmbedTexts(context.Background(), "test-key", []string{"hello"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing embeddings model")
}

func TestCreateEmbeddings_Batching(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		var reqBody embeddingsRequest
		json.NewDecoder(r.Body).Decode(&reqBody)

		data := make([]embeddingsDataItem, len(reqBody.Input))
		for i := range data {
			data[i] = embeddingsDataItem{Embedding: []float64{float64(i), 0.5}}
		}
		resp := embeddingsResponse{Data: data}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	// Create 35 inputs to trigger 2 batches (32 + 3).
	inputs := make([]string, 35)
	for i := range inputs {
		inputs[i] = "text"
	}

	e := NewOpenAIEmbedder(srv.URL, "test-model", srv.Client())
	vecs, err := e.EmbedTexts(context.Background(), "test-key", inputs)
	require.NoError(t, err)
	require.Len(t, vecs, 35)
	require.Equal(t, 2, callCount, "should make 2 batch requests")
}

func TestNormalizeBaseURL(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		// Bare host — unchanged.
		{"https://oneapi.laisky.com", "https://oneapi.laisky.com"},
		// Trailing slash.
		{"https://oneapi.laisky.com/", "https://oneapi.laisky.com"},
		// /v1 suffix.
		{"https://oneapi.laisky.com/v1", "https://oneapi.laisky.com"},
		{"https://api.openai.com/v1", "https://api.openai.com"},
		// /v1/ suffix.
		{"https://oneapi.laisky.com/v1/", "https://oneapi.laisky.com"},
		// /v1/embeddings suffix.
		{"https://oneapi.laisky.com/v1/embeddings", "https://oneapi.laisky.com"},
		// /v1/embeddings/ suffix.
		{"https://oneapi.laisky.com/v1/embeddings/", "https://oneapi.laisky.com"},
		// Whitespace + various suffixes.
		{"  https://example.com/v1/  ", "https://example.com"},
		{"  https://example.com/v1/embeddings/  ", "https://example.com"},
		// Path that merely contains "v1" but not as the suffix — preserved.
		{"https://example.com/api/v1proxy", "https://example.com/api/v1proxy"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := normalizeBaseURL(tt.input)
			require.Equal(t, tt.expected, got)
			// Also verify via constructor.
			e := NewOpenAIEmbedder(tt.input, "model", nil)
			require.Equal(t, tt.expected, e.baseURL)
		})
	}
}

// TestNewOpenAIEmbedder_V1BaseURL_CallsCorrectEndpoint verifies that even when
// the config includes "/v1" in base_url, the actual HTTP request goes to the
// correct /v1/embeddings path (not /v1/v1/embeddings).
func TestNewOpenAIEmbedder_V1BaseURL_CallsCorrectEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/embeddings", r.URL.Path,
			"should not double the /v1 prefix")
		resp := embeddingsResponse{
			Data: []embeddingsDataItem{
				{Embedding: []float64{0.1, 0.2}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	// Simulate config with /v1 suffix (common in OpenAI-compatible setups).
	e := NewOpenAIEmbedder(srv.URL+"/v1", "test-model", srv.Client())
	vecs, err := e.EmbedTexts(context.Background(), "test-key", []string{"hello"})
	require.NoError(t, err)
	require.Len(t, vecs, 1)
}

func TestNewOpenAIEmbedder_WithLogger(t *testing.T) {
	e := NewOpenAIEmbedder("http://localhost", "model", nil)
	require.Nil(t, e.logger, "logger should be nil by default")
}
