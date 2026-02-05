package userrequests

import (
	"strings"
	"unicode/utf8"

	errors "github.com/Laisky/errors/v2"
	"github.com/google/uuid"
)

const (
	// maxSearchQueryLength caps the length of user request search queries.
	maxSearchQueryLength    = 2048
	// maxRequestContentLength caps the length of request content payloads.
	maxRequestContentLength = 1024 * 50
)

// sanitizeSearchQuery trims and bounds a search query, returning the sanitized query or an error.
// It accepts the raw query string and returns the sanitized string.
func sanitizeSearchQuery(query string) (string, error) {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return "", errors.Wrap(ErrInvalidSearchQuery, "search query cannot be empty")
	}
	if strings.ContainsRune(trimmed, '\x00') {
		return "", errors.Wrap(ErrInvalidSearchQuery, "search query contains invalid null byte")
	}
	if utf8.RuneCountInString(trimmed) > maxSearchQueryLength {
		trimmed = string([]rune(trimmed)[:maxSearchQueryLength])
	}
	return trimmed, nil
}

// escapeLike escapes wildcard characters for SQL LIKE queries using backslash.
// It accepts the raw string and returns the escaped string.
func escapeLike(input string) string {
	replacer := strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_")
	return replacer.Replace(input)
}

// sanitizeListLimit clamps a list limit to the configured maximum and default.
// It accepts the requested limit and returns a safe limit value.
func sanitizeListLimit(limit int) int {
	if limit <= 0 {
		return defaultListLimit
	}
	if limit > defaultListLimit {
		return defaultListLimit
	}
	return limit
}

// sanitizeCursor validates a cursor string and returns a canonical UUID string or an error.
// It accepts the raw cursor string and returns the canonical UUID string.
func sanitizeCursor(cursor string) (string, error) {
	trimmed := strings.TrimSpace(cursor)
	if trimmed == "" {
		return "", nil
	}
	parsed, err := uuid.Parse(trimmed)
	if err != nil {
		return "", errors.Wrap(ErrInvalidCursor, "invalid cursor")
	}
	return parsed.String(), nil
}

// sanitizeRequestContent trims and bounds a request content string.
// It accepts the raw content string and returns the sanitized content or an error.
func sanitizeRequestContent(content string) (string, error) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return "", ErrEmptyContent
	}
	if strings.ContainsRune(trimmed, '\x00') {
		return "", errors.Wrap(ErrInvalidRequestContent, "request content contains invalid null byte")
	}
	if utf8.RuneCountInString(trimmed) > maxRequestContentLength {
		trimmed = string([]rune(trimmed)[:maxRequestContentLength])
	}
	return trimmed, nil
}
