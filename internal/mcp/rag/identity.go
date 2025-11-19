package rag

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"

	errors "github.com/Laisky/errors/v2"

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

// ParseIdentity derives multi-tenant identifiers from an Authorization header.
func ParseIdentity(header string) (*Identity, error) {
	trimmed := strings.TrimSpace(header)
	if trimmed == "" {
		return nil, errors.New("missing authorization bearer token")
	}

	raw := library.StripBearerPrefix(trimmed)
	if raw == "" {
		return nil, errors.New("missing authorization bearer token")
	}

	identityPart := ""
	token := raw
	if parts := strings.SplitN(raw, "@", 2); len(parts) == 2 {
		identityPart = strings.TrimSpace(parts[0])
		token = strings.TrimSpace(parts[1])
	}

	if token == "" {
		return nil, errors.New("invalid authorization header")
	}

	hash := sha256.Sum256([]byte(token))
	hashPrefix := hex.EncodeToString(hash[:])
	keyPrefix := token
	if len(keyPrefix) > 7 {
		keyPrefix = keyPrefix[:7]
	}

	userID := fmt.Sprintf("%s_%s", keyPrefix, hashPrefix[:7])
	taskID := sanitizeTaskID(identityPart)
	if taskID == "" {
		taskID = userID
	}

	masked := maskKey(token)

	return &Identity{
		APIKey:   token,
		UserID:   userID,
		TaskID:   taskID,
		KeyHash:  hashPrefix,
		MaskedID: masked,
	}, nil
}

func sanitizeTaskID(raw string) string {
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

func maskKey(token string) string {
	if len(token) <= 4 {
		return "***" + token
	}
	return "***" + token[len(token)-4:]
}
