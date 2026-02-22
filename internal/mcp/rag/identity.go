package rag

import (
	"regexp"
	"strings"

	errors "github.com/Laisky/errors/v2"

	mcpauth "github.com/Laisky/laisky-blog-graphql/internal/mcp/auth"
	"github.com/Laisky/laisky-blog-graphql/library"
)

// Identity encapsulates the derived identifiers for a given authorization header.
type Identity struct {
	APIKey   string
	UserID   string
	TaskID   string
	KeyHash  string
	MaskedID string
}

var safeTaskID = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

// SanitizeTaskID normalizes a task identifier by removing unsupported characters,
// trimming dashes, and enforcing a maximum length of 64 characters. Empty or
// fully stripped inputs return an empty string to signal invalid values.
func SanitizeTaskID(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	sanitized := safeTaskID.ReplaceAllString(trimmed, "-")
	sanitized = strings.Trim(sanitized, "-")
	if sanitized == "" {
		return ""
	}
	if len(sanitized) > 64 {
		return sanitized[:64]
	}
	return sanitized
}

// ParseIdentity derives multi-tenant identifiers from an Authorization header.
func ParseIdentity(header string) (*Identity, error) {
	trimmed := strings.TrimSpace(header)
	if trimmed == "" {
		return nil, errors.WithStack(mcpauth.ErrMissingAuthorization)
	}

	raw := library.StripBearerPrefix(trimmed)
	if raw == "" {
		return nil, errors.WithStack(mcpauth.ErrMissingAuthorization)
	}

	identityPart := ""
	token := raw
	if parts := strings.SplitN(raw, "@", 2); len(parts) == 2 {
		identityPart = strings.TrimSpace(parts[0])
		token = strings.TrimSpace(parts[1])
	}

	if token == "" {
		return nil, errors.WithStack(mcpauth.ErrInvalidAuthorization)
	}

	authCtx, err := mcpauth.DeriveFromAPIKey(token)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	userID := authCtx.UserID
	taskID := SanitizeTaskID(identityPart)
	if taskID == "" {
		taskID = userID
	}

	return &Identity{
		APIKey:   token,
		UserID:   userID,
		TaskID:   taskID,
		KeyHash:  authCtx.APIKeyHash,
		MaskedID: authCtx.MaskedKey(),
	}, nil
}
