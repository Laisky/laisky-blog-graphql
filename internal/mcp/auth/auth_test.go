package auth

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseAuthorizationContextSupportsLegacyIdentityPrefix(t *testing.T) {
	ctx, err := ParseAuthorizationContext("Bearer workspace@sk-test")
	require.NoError(t, err)
	require.Equal(t, "sk-test", ctx.APIKey)
	require.NotEmpty(t, ctx.APIKeyHash)
	require.Contains(t, ctx.UserID, "user:")
}

func TestParseAuthorizationContextKeepsEmailLikeTokenIntact(t *testing.T) {
	ctx, err := ParseAuthorizationContext("Bearer user@example.com")
	require.NoError(t, err)
	require.Equal(t, "user@example.com", ctx.APIKey)
}

func TestParseAuthorizationContextSupportsPlainToken(t *testing.T) {
	ctx, err := ParseAuthorizationContext("Bearer sk-plain-token")
	require.NoError(t, err)
	require.Equal(t, "sk-plain-token", ctx.APIKey)
}

func TestFromContextOrHeaderPrefersContext(t *testing.T) {
	seed, err := DeriveFromAPIKey("sk-from-context")
	require.NoError(t, err)

	ctx := WithContext(context.Background(), seed)
	resolved, err := FromContextOrHeader(ctx, "Bearer workspace@sk-from-header")
	require.NoError(t, err)
	require.Equal(t, "sk-from-context", resolved.APIKey)
}
