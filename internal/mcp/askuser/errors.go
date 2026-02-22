package askuser

import "github.com/Laisky/errors/v2"

var (
	// ErrMissingAuthorization indicates that the caller did not provide an Authorization header.
	ErrMissingAuthorization = errors.New("authorization header required")
	// ErrInvalidAuthorization indicates the Authorization header was present but malformed.
	ErrInvalidAuthorization = errors.New("invalid authorization header")
	// ErrRequestNotFound indicates the referenced request does not exist.
	ErrRequestNotFound = errors.New("ask_user request not found")
	// ErrForbidden indicates the caller is not allowed to operate on the request.
	ErrForbidden = errors.New("not allowed to operate on this request")
)
