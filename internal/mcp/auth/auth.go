package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	errors "github.com/Laisky/errors/v2"

	"github.com/Laisky/laisky-blog-graphql/internal/mcp/ctxkeys"
	"github.com/Laisky/laisky-blog-graphql/library"
)

var (
	// ErrMissingAuthorization indicates that no authorization header was provided.
	ErrMissingAuthorization = errors.New("authorization header required")
	// ErrInvalidAuthorization indicates that the authorization header is malformed.
	ErrInvalidAuthorization = errors.New("invalid authorization header")
	// ErrCredentialRejected indicates that the credential authority refused the API key:
	// it is unknown, revoked, expired, disabled, exhausted or not usable from this server.
	ErrCredentialRejected = errors.New("api key rejected")
	// ErrValidationUnavailable indicates that the API key could not be validated right now.
	// Callers must fail closed with a retryable status, never treat the key as valid.
	ErrValidationUnavailable = errors.New("api key validation temporarily unavailable")
)

// Context carries canonical MCP authorization metadata derived from API keys.
//
// Only an Authenticator produces a verified Context. FromContext ignores any
// other value, so no request path can derive a tenant from an unvalidated bearer.
type Context struct {
	RawHeader    string
	APIKey       string
	APIKeyHash   string
	KeySuffix    string
	UserID       string
	UserIdentity string
	AIIdentity   string
	// verified is set only by Authenticator.Authenticate.
	verified bool
}

// Verified reports whether the credential authority accepted this context's API key.
func (a *Context) Verified() bool {
	return a != nil && a.verified
}

// parseAuthorizationHeader parses an Authorization header and derives the
// tenant identifiers of the presented key. It is syntax only: the result is
// unverified until an Authenticator validates the key.
func parseAuthorizationHeader(header string) (*Context, error) {
	trimmedHeader := strings.TrimSpace(header)
	if trimmedHeader == "" {
		return nil, ErrMissingAuthorization
	}

	raw := library.StripBearerPrefix(trimmedHeader)
	if raw == "" {
		return nil, ErrInvalidAuthorization
	}

	trimmedToken := strings.TrimSpace(raw)
	if trimmedToken == "" {
		return nil, ErrInvalidAuthorization
	}
	tokenFields := strings.Fields(trimmedToken)
	if len(tokenFields) == 0 {
		return nil, ErrInvalidAuthorization
	}

	apiKey := normalizeAuthorizationToken(tokenFields[0])

	derived, err := DeriveFromAPIKey(apiKey)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	derived.RawHeader = trimmedHeader

	return derived, nil
}

// normalizeAuthorizationToken keeps backward compatibility for legacy headers that
// encode identity metadata as "<identity>@<api-key>" while preserving plain tokens.
func normalizeAuthorizationToken(token string) string {
	_, apiKey, ok := splitLegacyIdentityToken(token)
	if ok {
		return apiKey
	}

	return strings.TrimSpace(token)
}

// splitLegacyIdentityToken extracts the API key from a legacy "identity@token"
// authorization payload. It avoids splitting regular email-like tokens.
func splitLegacyIdentityToken(token string) (identity string, apiKey string, ok bool) {
	trimmed := strings.TrimSpace(token)
	if strings.Count(trimmed, "@") != 1 {
		return "", "", false
	}

	parts := strings.SplitN(trimmed, "@", 2)
	identity = strings.TrimSpace(parts[0])
	apiKey = strings.TrimSpace(parts[1])
	if identity == "" || apiKey == "" {
		return "", "", false
	}

	if strings.Contains(identity, " ") || strings.Contains(apiKey, " ") {
		return "", "", false
	}

	if strings.Contains(apiKey, ".") && !looksLikeAPIKey(apiKey) {
		return "", "", false
	}

	if !looksLikeAPIKey(apiKey) && !strings.Contains(identity, ":") {
		return "", "", false
	}

	return identity, apiKey, true
}

// looksLikeAPIKey reports whether the token uses a common API key prefix format.
func looksLikeAPIKey(token string) bool {
	lowered := strings.ToLower(strings.TrimSpace(token))
	prefixes := []string{"sk-", "rk-", "pk-", "ak-"}
	for _, prefix := range prefixes {
		if strings.HasPrefix(lowered, prefix) {
			return true
		}
	}

	return false
}

// DeriveFromAPIKey builds the canonical, unverified tenant identifiers of a raw API key.
// The result is never honored by FromContext; use an Authenticator to authenticate a caller.
func DeriveFromAPIKey(apiKey string) (*Context, error) {
	token := strings.TrimSpace(apiKey)
	if token == "" {
		return nil, ErrInvalidAuthorization
	}

	hashed := sha256.Sum256([]byte(token))
	apiKeyHash := hex.EncodeToString(hashed[:])
	userID := deriveUserID(apiKeyHash)

	return &Context{
		APIKey:       token,
		APIKeyHash:   apiKeyHash,
		KeySuffix:    keySuffix(token),
		UserID:       userID,
		UserIdentity: userID,
		AIIdentity:   userID,
	}, nil
}

// WithContext stores authorization context on a request context.
func WithContext(ctx context.Context, auth *Context) context.Context {
	if ctx == nil || auth == nil {
		return ctx
	}

	return context.WithValue(ctx, ctxkeys.AuthContext, auth)
}

// FromContext retrieves the verified authorization context of a request.
// Unverified contexts are ignored, so a caller can only obtain a tenant that
// the credential authority accepted.
func FromContext(ctx context.Context) (*Context, bool) {
	if ctx == nil {
		return nil, false
	}

	auth, ok := ctx.Value(ctxkeys.AuthContext).(*Context)
	if !ok || !auth.Verified() {
		return nil, false
	}

	return auth, true
}

// FromContextOrHeader returns the verified auth context of a request, or
// authenticates the header with the installed Authenticator.
func FromContextOrHeader(ctx context.Context, header string) (*Context, error) {
	if auth, ok := FromContext(ctx); ok {
		return auth, nil
	}

	// The client source of the validation budget comes from clientip.FromContext,
	// which also reads the gin context GraphQL resolvers run in.
	authenticated, err := Authenticate(ctx, header)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	return authenticated, nil
}

// MaskedKey returns a non-sensitive key suffix suitable for logs.
func (a *Context) MaskedKey() string {
	if a == nil || a.APIKey == "" {
		return ""
	}

	return fmt.Sprintf("***%s", keySuffix(a.APIKey))
}

// keySuffix returns the trailing key hint used for diagnostics.
func keySuffix(token string) string {
	if len(token) <= 4 {
		return token
	}

	return token[len(token)-4:]
}

// deriveUserID generates a stable tenant identifier from an API key hash.
func deriveUserID(apiKeyHash string) string {
	const userPrefix = "user:"
	const idLen = 16
	if len(apiKeyHash) < idLen {
		return userPrefix + apiKeyHash
	}

	return userPrefix + apiKeyHash[:idLen]
}
