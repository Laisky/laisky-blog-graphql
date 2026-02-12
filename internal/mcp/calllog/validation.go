package calllog

import (
	"strings"
	"unicode/utf8"

	errors "github.com/Laisky/errors/v2"
)

const (
	// maxToolNameLength caps tool name filter length.
	maxToolNameLength = 128
	// maxUserPrefixLength caps user key prefix filter length.
	maxUserPrefixLength = 128
)

// sanitizeOptionalText trims input, checks for null bytes, enforces maxLen runes, and returns the sanitized value.
// It accepts the raw input string, a rune length limit, and a field label for error context, returning the sanitized string.
func sanitizeOptionalText(input string, maxLen int, field string) (string, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return "", nil
	}
	if strings.ContainsRune(trimmed, '\x00') {
		return "", errors.Errorf("%s contains invalid null byte", field)
	}
	if utf8.RuneCountInString(trimmed) > maxLen {
		return "", errors.Errorf("%s exceeds max length %d", field, maxLen)
	}
	return trimmed, nil
}
