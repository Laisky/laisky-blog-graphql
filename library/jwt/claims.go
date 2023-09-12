package jwt

import (
	"github.com/Laisky/errors/v2"
	"github.com/Laisky/go-utils/v4"
	"github.com/golang-jwt/jwt/v4"
)

type UserClaims struct {
	jwt.RegisteredClaims
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
}

func (uc *UserClaims) Valid() error {
	now := utils.Clock.GetUTCNow()
	if !uc.RegisteredClaims.VerifyExpiresAt(now, true) {
		return errors.Errorf("token expired")
	}

	if !uc.RegisteredClaims.VerifyIssuedAt(now, true) {
		return errors.Errorf("token issueAt invalid")
	}

	return nil
}
