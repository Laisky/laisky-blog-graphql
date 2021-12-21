package jwt

import (
	"fmt"

	"github.com/Laisky/go-utils"
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
		return fmt.Errorf("token expired")
	}

	if !uc.RegisteredClaims.VerifyIssuedAt(now, true) {
		return fmt.Errorf("token issueAt invalid")
	}

	return nil
}
