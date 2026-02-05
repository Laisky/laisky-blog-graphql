package dao

import (
	"crypto/sha256"
	"crypto/subtle"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/Laisky/errors/v2"

	"github.com/Laisky/laisky-blog-graphql/internal/web/telegram/dto"
)

const (
	// maxAlertNameLength caps the length of alert type names.
	maxAlertNameLength  = 100
	// maxJoinKeyLength caps the length of join keys.
	maxJoinKeyLength    = 32
	// maxPushTokenLength caps the length of push tokens.
	maxPushTokenLength  = 64
	// maxMonitorNameLen caps the length of monitor user names.
	maxMonitorNameLen   = 100
	// maxNotesKeywordLen caps the length of notes search keywords.
	maxNotesKeywordLen  = 200
	// maxTelegramPageSize caps the number of rows returned per page.
	maxTelegramPageSize = 200
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

// sanitizeRequiredText trims input, enforces maxLen runes, and returns the sanitized value or an error.
// It accepts the raw input string, a rune length limit, and a field label, returning the sanitized string.
func sanitizeRequiredText(input string, maxLen int, field string) (string, error) {
	trimmed, err := sanitizeOptionalText(input, maxLen, field)
	if err != nil {
		return "", err
	}
	if trimmed == "" {
		return "", errors.Errorf("%s is required", field)
	}
	return trimmed, nil
}

// sanitizeAlertName validates an alert name and returns the sanitized value or an error.
// It accepts the raw alert name and returns the sanitized alert name string.
func sanitizeAlertName(name string) (string, error) {
	return sanitizeRequiredText(name, maxAlertNameLength, "alert name")
}

// sanitizeJoinKey validates a join key and returns the sanitized value or an error.
// It accepts the raw join key string and returns the sanitized join key.
func sanitizeJoinKey(key string) (string, error) {
	return sanitizeRequiredText(key, maxJoinKeyLength, "join key")
}

// sanitizePushToken validates a push token string and returns the sanitized value or an error.
// It accepts the raw token string and returns the sanitized token.
func sanitizePushToken(token string) (string, error) {
	return sanitizeRequiredText(token, maxPushTokenLength, "push token")
}

// sanitizeMonitorName validates an optional monitor name filter and returns the sanitized value or an error.
// It accepts the raw name string and returns the sanitized name string.
func sanitizeMonitorName(name string) (string, error) {
	return sanitizeOptionalText(name, maxMonitorNameLen, "monitor name")
}

// sanitizeTelegramPagination validates page and size bounds and returns the sanitized values or an error.
// It accepts the requested page and size and returns the validated page and size.
func sanitizeTelegramPagination(page int, size int) (int, int, error) {
	if page < 0 {
		return 0, 0, errors.New("page must be non-negative")
	}
	if size < 0 || size > maxTelegramPageSize {
		return 0, 0, errors.Errorf("size must be within [0~%d]", maxTelegramPageSize)
	}
	return page, size, nil
}

// sanitizeQueryCfg validates a query config and returns a sanitized copy.
// It accepts the raw query config and returns the sanitized config or an error.
func sanitizeQueryCfg(cfg *dto.QueryCfg) (*dto.QueryCfg, error) {
	if cfg == nil {
		return nil, errors.New("query config is nil")
	}
	normalized := *cfg
	page, size, err := sanitizeTelegramPagination(cfg.Page, cfg.Size)
	if err != nil {
		return nil, errors.Wrap(err, "sanitize pagination")
	}
	name, err := sanitizeMonitorName(cfg.Name)
	if err != nil {
		return nil, errors.Wrap(err, "sanitize name")
	}
	normalized.Page = page
	normalized.Size = size
	normalized.Name = name
	return &normalized, nil
}

// sanitizeNotesKeyword validates a notes search keyword and returns the sanitized value or an error.
// It accepts the raw keyword and returns the sanitized keyword string.
func sanitizeNotesKeyword(keyword string) (string, error) {
	trimmed, err := sanitizeRequiredText(keyword, maxNotesKeywordLen, "keyword")
	if err != nil {
		return "", err
	}
	if _, err = regexp.Compile(trimmed); err != nil {
		return "", errors.Wrap(err, "invalid keyword regexp")
	}
	return trimmed, nil
}

// secureCompareString performs a constant-time comparison of two strings and returns true when they match.
// It accepts two strings and returns a boolean indicating equality.
func secureCompareString(left string, right string) bool {
	leftSum := sha256.Sum256([]byte(left))
	rightSum := sha256.Sum256([]byte(right))
	return subtle.ConstantTimeCompare(leftSum[:], rightSum[:]) == 1
}
