package controller

import (
	"github.com/Laisky/errors/v2"

	"github.com/Laisky/laisky-blog-graphql/internal/web/blog/model"
)

const loginFailedMessage = "login failed"

// maskLoginError returns a sanitized login error for client responses.
// It accepts the raw error from the login flow and returns a safe error message.
func maskLoginError(err error) error {
	if err == nil {
		return nil
	}

	if errors.Is(err, model.ErrInvalidCredentials) {
		return errors.WithStack(model.ErrInvalidCredentials)
	}

	return errors.WithStack(errors.New(loginFailedMessage))
}
