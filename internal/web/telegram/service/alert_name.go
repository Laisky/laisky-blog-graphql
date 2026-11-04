package service

import (
	"github.com/Laisky/errors/v2"

	"github.com/Laisky/laisky-blog-graphql/internal/web/telegram/dao"
)

// NormalizeAlertName returns the canonical alert type name, the form alert_types
// stores and ValidateTokenForAlertType matches, or an error for a name that can
// never match. Per-type state such as the alert rate limit must be keyed by it.
func NormalizeAlertName(name string) (string, error) {
	canonical, err := dao.NormalizeAlertName(name)
	if err != nil {
		return "", errors.WithStack(err)
	}
	return canonical, nil
}

// NormalizePushToken returns the canonical push token, the form
// ValidateTokenForAlertType compares, or an error for a token that can never match.
func NormalizePushToken(token string) (string, error) {
	canonical, err := dao.NormalizePushToken(token)
	if err != nil {
		return "", errors.WithStack(err)
	}
	return canonical, nil
}

// AlertTypeNotFoundError returns the refusal ValidateTokenForAlertType gives for
// an alert type that does not exist; it matches ErrAlertUnauthorized.
func AlertTypeNotFoundError(alertType string) error {
	return dao.AlertTypeNotFoundError(alertType)
}

// AlertTokenMismatchError returns the refusal ValidateTokenForAlertType gives for
// a push token that does not match; it matches ErrAlertUnauthorized.
func AlertTokenMismatchError(alertType string) error {
	return dao.AlertTokenMismatchError(alertType)
}

// AlertCredentialChanges returns how many alert types this process has created
// or re-keyed, so an in-memory copy of the credentials can tell it is stale.
func AlertCredentialChanges() uint64 {
	return dao.AlertCredentialChanges()
}
