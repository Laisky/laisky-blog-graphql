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
