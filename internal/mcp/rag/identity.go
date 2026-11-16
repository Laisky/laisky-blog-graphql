package rag

import (
	"regexp"
	"strings"
)

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
