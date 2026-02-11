package files

import "strings"

// NormalizeContentEncoding returns the normalized encoding or an error.
func NormalizeContentEncoding(encoding string) (string, error) {
	if strings.TrimSpace(encoding) == "" {
		return "utf-8", nil
	}
	if strings.EqualFold(strings.TrimSpace(encoding), "utf-8") {
		return "utf-8", nil
	}
	return "", NewError(ErrCodeInvalidQuery, "content_encoding must be utf-8", false)
}

// ValidatePayloadSize enforces request payload size limits.
func ValidatePayloadSize(size int64, max int64) error {
	if max > 0 && size > max {
		return NewError(ErrCodePayloadTooLarge, "payload exceeds max size", false)
	}
	return nil
}

// ValidateFileSize enforces file size limits.
func ValidateFileSize(size int64, max int64) error {
	if max > 0 && size > max {
		return NewError(ErrCodePayloadTooLarge, "file exceeds max size", false)
	}
	return nil
}
