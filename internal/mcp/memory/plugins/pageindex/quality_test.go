package pageindex

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestInvalidRequestNeverReachesLLM prevents failed JSON encoding becoming a shared cache key.
func TestInvalidRequestNeverReachesLLM(t *testing.T) {
	for _, v := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		llm := NewStubLLM()
		llm.SetDefault(TextResponse("should not run"))
		cache, err := NewCache(CacheConfig{Enabled: false})
		require.NoError(t, err)
		idx := &Indexer{llm: llm, cache: cache}
		_, err = idx.callLLM(t.Context(), Request{Temperature: float32(v)}, nil, &Stats{})
		require.Error(t, err)
		require.Zero(t, llm.CallCount())
	}
}

// TestRequestHashesIncludeModel preserves deterministic, model-specific cache keys.
func TestRequestHashesIncludeModel(t *testing.T) {
	a := Request{Model: "a", Input: []InputItem{{Role: "user", Content: "test"}}}
	ha, err := HashRequest(a)
	require.NoError(t, err)
	same, err := HashRequest(a)
	require.NoError(t, err)
	require.Equal(t, ha, same)
	a.Model = "b"
	hb, err := HashRequest(a)
	require.NoError(t, err)
	require.NotEqual(t, ha, hb)
}
