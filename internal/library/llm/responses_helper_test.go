package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestResponsesHelperCreateText verifies helper parses output_text and sends expected request shape.
func TestResponsesHelperCreateText(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/v1/responses", r.URL.Path)
		require.Equal(t, "Bearer sk-test", r.Header.Get("Authorization"))

		var payload map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		require.Equal(t, "openai/gpt-oss-120b", payload["model"])
		require.Equal(t, "hello", payload["input"])

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"output_text":"ok"}`))
	}))
	defer server.Close()

	helper := NewResponsesHelper(server.URL, 2*time.Second, nil)
	text, err := helper.CreateText(context.Background(), "sk-test", ResponseRequest{
		Model: "openai/gpt-oss-120b",
		Input: "hello",
	})
	require.NoError(t, err)
	require.Equal(t, "ok", text)
}

// TestResponsesCreateResponseAggregatedText verifies fallback aggregation from output content.
func TestResponsesCreateResponseAggregatedText(t *testing.T) {
	t.Parallel()

	resp := responsesCreateResponse{
		Output: []responsesOutputItem{
			{
				Type: "message",
				Content: []responsesOutputContent{
					{Type: "output_text", Text: "line1"},
					{Type: "text", Text: "line2"},
				},
			},
		},
	}

	require.Equal(t, "line1\nline2", resp.AggregatedText())
}
