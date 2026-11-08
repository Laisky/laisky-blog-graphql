package askuser

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/auth/authtest"
)

// TestMain installs an explicit accept-all authenticator: these tests use
// synthetic bearers and must not depend on one-api.
func TestMain(m *testing.M) {
	authtest.InstallAcceptAll()
	os.Exit(m.Run())
}

func TestParseAuthorizationFromContext(t *testing.T) {
	tests := map[string]struct {
		header      string
		expectError bool
		expectKey   string
	}{
		"bearer prefix": {
			header:    "Bearer abcdefghijklmnop",
			expectKey: "abcdefghijklmnop",
		},
		"no prefix": {
			header:    "token-123",
			expectKey: "token-123",
		},
		"mixed case prefix": {
			header:    "bEaReR   spacedtoken ",
			expectKey: "spacedtoken",
		},
		"with at symbol": {
			header:    "Bearer user@example.com",
			expectKey: "user@example.com",
		},
		"missing token": {
			header:      "Bearer   \t",
			expectError: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, err := ParseAuthorizationFromContext(context.Background(), tc.header)
			if tc.expectError {
				require.Error(t, err, "expected error")
				return
			}
			require.NoError(t, err, "unexpected error")
			require.Equal(t, tc.expectKey, ctx.APIKey, "unexpected key")
			require.NotEmpty(t, ctx.APIKeyHash, "api key hash should be derived")
			require.Equal(t, ctx.UserID, ctx.UserIdentity, "user identity should equal canonical user id")
			require.Equal(t, ctx.UserID, ctx.AIIdentity, "ai identity should equal canonical user id")
			require.Contains(t, ctx.UserID, "user:", "user id should be tenant-prefixed")
		})
	}
}
