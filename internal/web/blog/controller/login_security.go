package controller

import (
	"context"
	"unicode/utf8"

	"github.com/Laisky/errors/v2"
	ginMw "github.com/Laisky/gin-middlewares/v7"
	"github.com/Laisky/zap"

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

// credentialFailure classifies an error from a credential check. It returns the
// error to show the client and whether the failure is attributable to the
// submitted credentials, which is the only kind that may count toward the
// anti-abuse failure threshold.
//
// ErrInvalidCredentials stays ErrInvalidCredentials. Every other error means the
// credentials could not be evaluated (database or signing failure), so it is
// reported as ErrLoginUnavailable and never counted against the client.
func credentialFailure(ctx context.Context, method string, err error) (clientErr error, countsAsFailure bool) {
	logger := ginMw.GetLogger(ctx).Named("sso_login")
	if errors.Is(err, model.ErrInvalidCredentials) {
		logger.Debug("sso credential check rejected",
			zap.String("method", method),
			zap.String("class", "invalid_credentials"),
		)
		return errors.WithStack(model.ErrInvalidCredentials), true
	}

	logger.Error("sso credential check could not be evaluated",
		zap.String("method", method),
		zap.String("class", "unavailable"),
		zap.Error(err),
	)
	return errors.WithStack(model.ErrLoginUnavailable), false
}

// validateInputLength checks if the provided inputs are within the length limit.
func validateInputLength(limit int, inputs ...string) error {
	for _, input := range inputs {
		if utf8.RuneCountInString(input) > limit {
			return errors.Errorf("input too long: max %d characters allowed", limit)
		}
	}
	return nil
}
