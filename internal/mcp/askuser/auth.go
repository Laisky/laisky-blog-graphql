package askuser

import (
	"context"
	"strconv"

	"github.com/Laisky/errors/v2"

	mcpauth "github.com/Laisky/laisky-blog-graphql/internal/mcp/auth"
)

// AuthorizationContext is the caller identity of the ask_user and
// user-request packages. It is the shared mcpauth.Context itself, not a copy,
// so the verified flag travels with it: a value from ParseAuthorizationFromContext
// or FromSharedContext is one the credential authority accepted
// (Verified() == true), and the only key-less principal is the explicit
// NewTelegramAnswerer. New fields belong on mcpauth.Context.
type AuthorizationContext = mcpauth.Context

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

	return shared, nil
}

// FromSharedContext is the checked conversion of a shared MCP auth context
// into an ask_user identity: it returns shared only when the credential
// authority verified it, and nil otherwise (including for nil). Callers must
// handle nil as "no caller".
func FromSharedContext(shared *mcpauth.Context) *AuthorizationContext {
	if !shared.Verified() {
		return nil
	}
	return shared
}

// NewTelegramAnswerer returns the one delegated, key-less principal of the
// ask_user answer path: a Telegram user answering a question for the API key
// whose hash apiKeyHash is bound to telegramUID. The caller must have checked
// that binding (the binding was created by proving possession of a key the
// credential authority accepted). The principal is unverified by design and
// carries no key, so it never satisfies mcpauth.FromContext and can only act
// where a service accepts an explicit answerer for apiKeyHash.
func NewTelegramAnswerer(apiKeyHash string, telegramUID int64) *AuthorizationContext {
	return &AuthorizationContext{
		APIKeyHash:   apiKeyHash,
		UserIdentity: "telegram:" + strconv.FormatInt(telegramUID, 10),
	}
}
