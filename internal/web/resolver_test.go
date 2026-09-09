package web

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestResolverExtractKeyInfoWithoutRAGService guards the typed-nil trap: the
// RAG mutation must report a clean error when the service is unavailable
// instead of panicking inside the resolver dispatch.
func TestResolverExtractKeyInfoWithoutRAGService(t *testing.T) {
	// Build the mutation resolver directly: NewResolver additionally spins up
	// the Telegram controller, which needs a full runtime configuration.
	resolver := (&Resolver{}).buildMutationResolver()
	require.NotNil(t, resolver)

	got, err := resolver.ExtractKeyInfo(context.Background(), "q", "materials", nil)
	require.Error(t, err)
	require.Nil(t, got)
	require.ErrorContains(t, err, "rag service is not configured")
}
