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

// ParseAuthorizationFromContext returns the verified caller of a request: the
// auth context installed by an MCP gate, or the header authenticated by the
// installed credential authority. The token is the sole source of identity.
func ParseAuthorizationFromContext(ctx context.Context, header string) (*AuthorizationContext, error) {
	shared, err := mcpauth.FromContextOrHeader(ctx, header)
	if err != nil {
		// Callers echo these messages to clients, so they stay stable sentinels.
		switch {
		case errors.Is(err, mcpauth.ErrMissingAuthorization):
			return nil, errors.WithStack(ErrMissingAuthorization)
		case errors.Is(err, mcpauth.ErrInvalidAuthorization):
			return nil, errors.WithStack(ErrInvalidAuthorization)
		case errors.Is(err, mcpauth.ErrValidationUnavailable):
			return nil, errors.WithStack(mcpauth.ErrValidationUnavailable)
		default:
			return nil, errors.WithStack(mcpauth.ErrCredentialRejected)
		}
	}

	return FromSharedContext(shared), nil
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

// FromSharedContext converts the shared MCP auth context into ask_user auth context.
func FromSharedContext(shared *mcpauth.Context) *AuthorizationContext {
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
