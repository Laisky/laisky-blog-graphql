package files

import (
	"fmt"

	errors "github.com/Laisky/errors/v2"
)

// ErrorCode identifies a machine-stable file error code.
type ErrorCode string

const (
	ErrCodeNotFound         ErrorCode = "NOT_FOUND"
	ErrCodeAlreadyExists    ErrorCode = "ALREADY_EXISTS"
	ErrCodeIsDirectory      ErrorCode = "IS_DIRECTORY"
	ErrCodeNotDirectory     ErrorCode = "NOT_DIRECTORY"
	ErrCodeInvalidPath      ErrorCode = "INVALID_PATH"
	ErrCodeInvalidOffset    ErrorCode = "INVALID_OFFSET"
	ErrCodeInvalidQuery     ErrorCode = "INVALID_QUERY"
	ErrCodeNotEmpty         ErrorCode = "NOT_EMPTY"
	ErrCodePermissionDenied ErrorCode = "PERMISSION_DENIED"
	ErrCodePayloadTooLarge  ErrorCode = "PAYLOAD_TOO_LARGE"
	ErrCodeQuotaExceeded    ErrorCode = "QUOTA_EXCEEDED"
	ErrCodeRateLimited      ErrorCode = "RATE_LIMITED"
	ErrCodeResourceBusy     ErrorCode = "RESOURCE_BUSY"
	ErrCodeSearchBackend    ErrorCode = "SEARCH_BACKEND_ERROR"
)

// Error captures a typed file error with retryability metadata.
type Error struct {
	Code      ErrorCode
	Message   string
	Retryable bool
}

// Error returns the error message.
func (e *Error) Error() string {
	if e == nil {
		return "file error: <nil>"
	}
	if e.Message == "" {
		return fmt.Sprintf("file error: %s", e.Code)
	}
	return e.Message
}

// NewError constructs a typed file error.
func NewError(code ErrorCode, message string, retryable bool) *Error {
	return &Error{Code: code, Message: message, Retryable: retryable}
}

// AsError extracts a typed file error from the error chain.
func AsError(err error) (*Error, bool) {
	if err == nil {
		return nil, false
	}
	var typed *Error
	if errors.As(err, &typed) {
		return typed, true
	}
	return nil, false
}

// IsCode reports whether the error chain contains the given code.
func IsCode(err error, code ErrorCode) bool {
	if err == nil {
		return false
	}
	if typed, ok := AsError(err); ok {
		return typed.Code == code
	}
	return false
}
