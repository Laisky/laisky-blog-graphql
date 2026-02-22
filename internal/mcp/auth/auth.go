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
)

// Context carries canonical MCP authorization metadata derived from API keys.
type Context struct {
	RawHeader    string
	APIKey       string
	APIKeyHash   string
	KeySuffix    string
	UserID       string
	UserIdentity string
	AIIdentity   string
}

// ParseAuthorizationContext parses an Authorization header and derives tenant identifiers.
func ParseAuthorizationContext(header string) (*Context, error) {
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

// DeriveFromAPIKey builds a canonical authorization context from a raw API key.
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

// FromContext retrieves authorization context from a request context.
func FromContext(ctx context.Context) (*Context, bool) {
	if ctx == nil {
		return nil, false
	}

	auth, ok := ctx.Value(ctxkeys.AuthContext).(*Context)
	if !ok || auth == nil {
		return nil, false
	}

	return auth, true
}

// FromContextOrHeader returns auth context from request context, falling back to parsing a header.
func FromContextOrHeader(ctx context.Context, header string) (*Context, error) {
	if auth, ok := FromContext(ctx); ok {
		return auth, nil
	}

	parsed, err := ParseAuthorizationContext(header)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	return parsed, nil
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
