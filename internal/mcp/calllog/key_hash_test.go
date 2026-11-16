package calllog

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/auth/authtest"
)

// TestNormalizeAPIKeyUsesTheSharedTenantHash pins that the stored call-log
// hash is the verified caller's APIKeyHash (the value the console filters
// by) and stays byte-identical to the historical sha256(TrimSpace(key)), so
// existing rows keep their owner.
func TestNormalizeAPIKeyUsesTheSharedTenantHash(t *testing.T) {
	for _, key := range []string{"sk-alpha", "  sk-padded  ", "abc", "sk-0123456789abcdef"} {
		hash, prefix := normalizeAPIKey(key)
		trimmed := strings.TrimSpace(key)
		historical := sha256.Sum256([]byte(trimmed))
		require.Equal(t, hex.EncodeToString(historical[:]), hash, key)
		require.Equal(t, authtest.Verified(trimmed).APIKeyHash, hash, key)
		require.Equal(t, trimmed[:min(len(trimmed), 7)], prefix, key)
	}

	hash, prefix := normalizeAPIKey("   ")
	require.Empty(t, hash)
	require.Empty(t, prefix)
}
