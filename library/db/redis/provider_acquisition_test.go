package redis

import (
	"encoding/json"
	"testing"

	"github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// TestCrawlerProducerAdvertisesProviderFallback verifies that the actual queue
// envelope opts into provider results without adding caller credentials.
func TestCrawlerProducerAdvertisesProviderFallback(t *testing.T) {
	server := miniredis.RunT(t)
	db := NewDB(&redis.Options{Addr: server.Addr(), Protocol: 2, MaxRetries: -1, DisableIdentity: true})
	t.Cleanup(func() { require.NoError(t, db.GetDB().Close()) })
	id, err := db.AddHTMLCrawlerTaskWithOptions(t.Context(), "https://1.1.1.1/article", "synthetic-caller-key", true)
	require.NoError(t, err)
	pending, err := server.List(KeyTaskHTMLCrawlerPending)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	var envelope map[string]any
	require.NoError(t, json.Unmarshal([]byte(pending[0]), &envelope))
	require.Equal(t, id, envelope["task_id"])
	require.Equal(t, true, envelope["allow_provider_fallback"])
	require.NotContains(t, envelope, "api_key")
	require.NotContains(t, envelope, "acquisition", "only the worker reports the actual acquisition method")
}
