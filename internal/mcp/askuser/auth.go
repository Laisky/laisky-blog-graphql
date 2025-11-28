package askuser

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/Laisky/laisky-blog-graphql/library"
)

// AuthorizationContext captures caller identity derived from the Authorization header.
type AuthorizationContext struct {
	RawHeader    string
	APIKey       string
	APIKeyHash   string
	KeySuffix    string
	UserIdentity string
	AIIdentity   string
}

// ParseAuthorizationContext extracts API key material from an Authorization
// header that may or may not include the Bearer prefix. The token itself is the
// sole source of truth for the caller's identity.
func ParseAuthorizationContext(header string) (*AuthorizationContext, error) {
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
	token := tokenFields[0]

	userID := deriveUserIdentity(token)
	aiID := userID

	hashed := sha256.Sum256([]byte(token))
	suffix := ""
	if l := len(token); l > 0 {
		if l <= 4 {
			suffix = token
		} else {
			suffix = token[l-4:]
		}
	}

	return &AuthorizationContext{
		RawHeader:    trimmedHeader,
		APIKey:       token,
		APIKeyHash:   hex.EncodeToString(hashed[:]),
		KeySuffix:    suffix,
		UserIdentity: userID,
		AIIdentity:   aiID,
	}, nil
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

func deriveUserIdentity(token string) string {
	const prefixLength = 8
	identity := token
	if len(identity) > prefixLength {
		identity = identity[:prefixLength]
	}
	if identity == "" {
		return "user:anonymous"
	}
	return fmt.Sprintf("user:%s", identity)
}
