package askuser

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	mcpauth "github.com/Laisky/laisky-blog-graphql/internal/mcp/auth"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/auth/authtest"
)

// TestFromSharedContextRefusesUnverified pins the checked conversion the
// third maintainer review of PR #95 asked for: only a context the credential
// authority verified becomes an ask_user identity.
func TestFromSharedContextRefusesUnverified(t *testing.T) {
	unverified, err := mcpauth.DeriveFromAPIKey("sk-unverified")
	require.NoError(t, err)
	require.Nil(t, FromSharedContext(unverified))
	require.Nil(t, FromSharedContext(&mcpauth.Context{APIKey: "sk-literal", APIKeyHash: "hash"}))
	require.Nil(t, FromSharedContext(nil))
}

// TestFromSharedContextKeepsTheVerifiedIdentity: the conversion is the shared
// context itself, so the verified flag and every field survive.
func TestFromSharedContextKeepsTheVerifiedIdentity(t *testing.T) {
	shared := authtest.Verified("sk-verified-key")
	got := FromSharedContext(shared)
	require.Same(t, shared, got, "no copy: one representation of the caller")
	require.True(t, got.Verified())

	parsed, err := ParseAuthorizationFromContext(mcpauth.WithContext(context.Background(), shared), "")
	require.NoError(t, err)
	require.Same(t, shared, parsed)
	require.True(t, parsed.Verified())
}

// TestMaskedKeyMatchesTheRemovedAskUserHelper pins that the shared helper
// prints what askuser's own MaskedKey did: "***" plus the last four bytes, or
// the whole key when it has at most four.
func TestMaskedKeyMatchesTheRemovedAskUserHelper(t *testing.T) {
	legacy := func(key string) string {
		if key == "" {
			return ""
		}
		if len(key) <= 4 {
			return "***" + key
		}
		return "***" + key[len(key)-4:]
	}
	for _, key := range []string{"", "a", "abcd", "abcde", "sk-0123456789"} {
		require.Equal(t, legacy(key), (&AuthorizationContext{APIKey: key}).MaskedKey(), key)
	}
	var none *AuthorizationContext
	require.Empty(t, none.MaskedKey())
}

// TestTelegramAnswererIsKeylessAndUnverified: the delegated principal carries
// the bound hash and the Telegram identity only, and never authenticates a
// request context.
func TestTelegramAnswererIsKeylessAndUnverified(t *testing.T) {
	answerer := NewTelegramAnswerer("bound-hash", 4242)
	require.Equal(t, "bound-hash", answerer.APIKeyHash)
	require.Equal(t, "telegram:4242", answerer.UserIdentity)
	require.Empty(t, answerer.APIKey)
	require.Empty(t, answerer.RawHeader)
	require.False(t, answerer.Verified())
	_, ok := mcpauth.FromContext(mcpauth.WithContext(context.Background(), answerer))
	require.False(t, ok)
	require.Nil(t, FromSharedContext(answerer))
}
