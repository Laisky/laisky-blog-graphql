package files

import (
	mcpauth "github.com/Laisky/laisky-blog-graphql/internal/mcp/auth"
)

// AuthFromVerified is the single conversion from the shared caller identity
// to the files package's AuthContext. It returns false, and an empty
// AuthContext (which every service method refuses as "missing api key"), for
// a nil context or one the credential authority did not verify, so no file
// tenant is ever derived from an unvalidated bearer.
func AuthFromVerified(shared *mcpauth.Context) (AuthContext, bool) {
	if !shared.Verified() {
		return AuthContext{}, false
	}
	return AuthContext{
		APIKey:       shared.APIKey,
		APIKeyHash:   shared.APIKeyHash,
		UserID:       shared.UserID,
		UserIdentity: shared.UserIdentity,
	}, true
}
