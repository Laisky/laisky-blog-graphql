package askuser

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseAuthorizationContext(t *testing.T) {
	tests := map[string]struct {
		header      string
		expectError bool
		expectKey   string
		expectUser  string
	}{
		"bearer prefix": {
			header:     "Bearer abcdefghijklmnop",
			expectKey:  "abcdefghijklmnop",
			expectUser: "user:abcdefgh",
		},
		"no prefix": {
			header:     "token-123",
			expectKey:  "token-123",
			expectUser: "user:token-12",
		},
		"mixed case prefix": {
			header:     "bEaReR   spacedtoken ",
			expectKey:  "spacedtoken",
			expectUser: "user:spacedto",
		},
		"with at symbol": {
			header:     "Bearer user@example.com",
			expectKey:  "user@example.com",
			expectUser: "user:user@exa",
		},
		"missing token": {
			header:      "Bearer   \t",
			expectError: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, err := ParseAuthorizationContext(tc.header)
			if tc.expectError {
				require.Error(t, err, "expected error")
				return
			}
			require.NoError(t, err, "unexpected error")
			require.Equal(t, tc.expectKey, ctx.APIKey, "unexpected key")
			require.Equal(t, tc.expectUser, ctx.UserIdentity, "unexpected user identity")
			require.Equal(t, tc.expectUser, ctx.AIIdentity, "unexpected ai identity")
		})
	}
}
