package userrequests

import errors "github.com/Laisky/errors/v2"

var (
	// ErrInvalidAuthorization indicates the caller did not provide a valid authorization context.
	ErrInvalidAuthorization = errors.New("invalid authorization context")
	// ErrRequestNotFound is returned when a referenced request cannot be located for the authenticated user.
	ErrRequestNotFound = errors.New("user request not found")
	// ErrNoPendingRequests indicates there are no pending directives for the authenticated user.
	ErrNoPendingRequests = errors.New("no pending user requests")
	// ErrEmptyContent indicates the payload provided by the human operator was empty.
	ErrEmptyContent = errors.New("request content cannot be empty")
	// ErrSavedCommandNotFound is returned when a referenced saved command cannot be located for the authenticated user.
	ErrSavedCommandNotFound = errors.New("saved command not found")
	// ErrSavedCommandLimitReached indicates the user has reached the maximum number of saved commands.
	ErrSavedCommandLimitReached = errors.New("saved command limit reached")
)
