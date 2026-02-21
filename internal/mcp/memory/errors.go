package memory

import (
	"fmt"

	errors "github.com/Laisky/errors/v2"
)

// ErrorCode identifies a stable memory-tool error category.
type ErrorCode string

const (
	ErrCodeInvalidArgument  ErrorCode = "INVALID_ARGUMENT"
	ErrCodePermissionDenied ErrorCode = "PERMISSION_DENIED"
	ErrCodeResourceBusy     ErrorCode = "RESOURCE_BUSY"
	ErrCodeInternal         ErrorCode = "INTERNAL_ERROR"
)

// Error carries a machine-readable code and retryable attribute.
type Error struct {
	Code      ErrorCode
	Message   string
	Retryable bool
}

// Error renders the error text.
func (e *Error) Error() string {
	if e == nil {
		return "memory error: <nil>"
	}
	if e.Message == "" {
		return fmt.Sprintf("memory error: %s", e.Code)
	}
	return e.Message
}

// NewError constructs a memory typed error.
func NewError(code ErrorCode, message string, retryable bool) *Error {
	return &Error{Code: code, Message: message, Retryable: retryable}
}

// AsError returns a typed memory error from an error chain when available.
func AsError(err error) (*Error, bool) {
	if err == nil {
		return nil, false
	}
	typed := &Error{}
	if errors.As(err, &typed) {
		return typed, true
	}
	return nil, false
}
