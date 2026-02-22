package askuser

import (
	"context"
	"fmt"

	"github.com/Laisky/errors/v2"
	mcpauth "github.com/Laisky/laisky-blog-graphql/internal/mcp/auth"
)

// AuthorizationContext captures caller identity derived from the Authorization header.
type AuthorizationContext struct {
	RawHeader    string
	APIKey       string
	APIKeyHash   string
	KeySuffix    string
	UserID       string
	UserIdentity string
	AIIdentity   string
}

// ParseAuthorizationContext extracts API key material from an Authorization
// header that may or may not include the Bearer prefix. The token itself is the
// sole source of truth for the caller's identity.
func ParseAuthorizationContext(header string) (*AuthorizationContext, error) {
	parsed, err := mcpauth.ParseAuthorizationContext(header)
	if err != nil {
		if errors.Is(err, mcpauth.ErrMissingAuthorization) {
			return nil, ErrMissingAuthorization
		}
		if errors.Is(err, mcpauth.ErrInvalidAuthorization) {
			return nil, ErrInvalidAuthorization
		}
		return nil, err
	}

	return fromSharedContext(parsed), nil
}

// ParseAuthorizationFromContext retrieves authorization from context and falls back to the provided header.
func ParseAuthorizationFromContext(ctx context.Context, header string) (*AuthorizationContext, error) {
	if shared, ok := mcpauth.FromContext(ctx); ok {
		return fromSharedContext(shared), nil
	}

	return ParseAuthorizationContext(header)
}

// MaskedKey returns a short identifier for logging purposes.
func (a *AuthorizationContext) MaskedKey() string {
	if a == nil || a.APIKey == "" {
		return ""
	}
	if len(a.APIKey) <= 4 {
		return fmt.Sprintf("***%s", a.APIKey)
	}
	return fmt.Sprintf("***%s", a.APIKey[len(a.APIKey)-4:])
}

// fromSharedContext converts the shared MCP auth context into ask_user auth context.
func fromSharedContext(shared *mcpauth.Context) *AuthorizationContext {
	if shared == nil {
		return nil
	}

	return &AuthorizationContext{
		RawHeader:    shared.RawHeader,
		APIKey:       shared.APIKey,
		APIKeyHash:   shared.APIKeyHash,
		KeySuffix:    shared.KeySuffix,
		UserID:       shared.UserID,
		UserIdentity: shared.UserIdentity,
		AIIdentity:   shared.AIIdentity,
	}
}
