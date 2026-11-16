package tools

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	mcpauth "github.com/Laisky/laisky-blog-graphql/internal/mcp/auth"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/auth/authtest"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/ctxkeys"
	"github.com/Laisky/laisky-blog-graphql/internal/mcp/files"
)

// TestFileAuthFromContextRefusesUnverifiedIdentity reproduces finding F2 of
// the PR #95 credential-ingress inventory: a file tool must derive its tenant
// only from a context the credential authority verified. Neither an
// unverified shared context nor a files.AuthContext planted under the auth
// context key may yield a tenant.
func TestFileAuthFromContextRefusesUnverifiedIdentity(t *testing.T) {
	planted := context.WithValue(context.Background(), ctxkeys.AuthContext, &files.AuthContext{APIKey: "key", APIKeyHash: "hash"})
	_, ok := fileAuthFromContext(planted)
	require.False(t, ok, "a files.AuthContext under the auth key is no verified identity")

	unverified, err := mcpauth.DeriveFromAPIKey("sk-unverified")
	require.NoError(t, err)
	_, ok = fileAuthFromContext(mcpauth.WithContext(context.Background(), unverified))
	require.False(t, ok, "an unverified shared context yields no tenant")

	_, ok = fileAuthFromContext(context.Background())
	require.False(t, ok)

	verified := authtest.Verified("sk-verified")
	auth, ok := fileAuthFromContext(authtest.WithVerified(context.Background(), "sk-verified"))
	require.True(t, ok)
	require.Equal(t, files.AuthContext{APIKey: verified.APIKey, APIKeyHash: verified.APIKeyHash, UserID: verified.UserID, UserIdentity: verified.UserIdentity}, auth)
}
