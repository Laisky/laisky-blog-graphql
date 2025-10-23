package askuser

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
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

// ParseAuthorizationContext extracts identities and API key material from a Bearer token.
//
// The header supports the following formats:
//  1. "Bearer <token>" - the token itself is treated as the user's API key and
//     both the user and AI identities default to derived values.
//  2. "Bearer <identity>@<token>" - the string before the last '@' is treated as
//     a combined identity descriptor. If it contains a ':' character the part
//     before ':' becomes the user identity and the remaining suffix becomes the
//     AI identity. When ':' is absent, the descriptor is used for both.
//
// The final component after the last '@' is always considered the actual API key
// that will be forwarded to downstream services (for example, billing).
func ParseAuthorizationContext(header string) (*AuthorizationContext, error) {
	header = strings.TrimSpace(header)
	if header == "" {
		return nil, ErrMissingAuthorization
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(strings.ToLower(header), strings.ToLower(prefix)) {
		return nil, ErrInvalidAuthorization
	}

	raw := strings.TrimSpace(header[len(prefix):])
	if raw == "" {
		return nil, ErrInvalidAuthorization
	}

	token := raw
	identitySegment := ""
	if idx := strings.LastIndex(raw, "@"); idx >= 0 {
		identitySegment = strings.TrimSpace(raw[:idx])
		token = strings.TrimSpace(raw[idx+1:])
	}

	if token == "" {
		return nil, ErrInvalidAuthorization
	}

	userID := "user:anonymous"
	aiID := "ai:unknown"
	if identitySegment != "" {
		switch parts := strings.Split(identitySegment, ":"); len(parts) {
		case 0:
			// no-op
		case 1:
			segment := strings.TrimSpace(parts[0])
			if segment != "" {
				userID = segment
				aiID = segment
			}
		default:
			user := strings.TrimSpace(parts[0])
			ai := strings.TrimSpace(strings.Join(parts[1:], ":"))
			if user != "" {
				userID = user
			}
			if ai != "" {
				aiID = ai
			}
		}
	}

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
		RawHeader:    header,
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
