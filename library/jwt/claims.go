package jwt

import (
	"fmt"

	"github.com/Laisky/go-utils"
	"github.com/form3tech-oss/jwt-go"
)

type UserClaims struct {
	jwt.StandardClaims
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
}

func (uc *UserClaims) Valid() error {
	now := utils.Clock.GetUTCNow().Unix()
	if !uc.StandardClaims.VerifyExpiresAt(now, true) {
		return fmt.Errorf("token expired")
	}

	if !uc.StandardClaims.VerifyIssuedAt(now, true) {
		return fmt.Errorf("token issueAt invalid")
	}

	return nil
}
