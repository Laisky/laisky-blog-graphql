package askuser

import "testing"

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
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ctx.APIKey != tc.expectKey {
				t.Fatalf("expected key %q, got %q", tc.expectKey, ctx.APIKey)
			}
			if ctx.UserIdentity != tc.expectUser {
				t.Fatalf("expected user identity %q, got %q", tc.expectUser, ctx.UserIdentity)
			}
			if ctx.AIIdentity != tc.expectUser {
				t.Fatalf("expected ai identity %q, got %q", tc.expectUser, ctx.AIIdentity)
			}
		})
	}
}
