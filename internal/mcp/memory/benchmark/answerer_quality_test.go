package benchmark

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestReaderEvidenceNumbersRemainDecimal validates labels beyond the first nine hits through real HTTP encoding.
func TestReaderEvidenceNumbersRemainDecimal(t *testing.T) {
	var captured struct {
		Input []struct {
			Content string `json:"content"`
		} `json:"input"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Error(err)
		}
		_, _ = w.Write([]byte(`{"output_text":"ok"}`))
	}))
	defer srv.Close()
	a, err := NewResponsesAnswerer(srv.URL, "synthetic", "test", srv.Client())
	require.NoError(t, err)
	hits := make([]SearchHit, 12)
	for i := range hits {
		hits[i] = SearchHit{FilePath: fmt.Sprintf("f%d", i), Content: "memory"}
	}
	_, err = a.Answer(context.Background(), Query{Text: "q"}, hits)
	require.NoError(t, err)
	require.Len(t, captured.Input, 2)
	for i := 1; i <= 12; i++ {
		require.Contains(t, captured.Input[1].Content, fmt.Sprintf("--- MEMORY %d [", i))
	}
	require.Equal(t, "reader-v2", ReaderPromptVersion)
}
