package jwt

import (
	"time"

	"github.com/Laisky/errors/v2"
	"github.com/Laisky/go-utils/v5"
	gutils "github.com/Laisky/go-utils/v5"
	jwtLib "github.com/golang-jwt/jwt/v4"
)

type UserClaims struct {
	jwtLib.RegisteredClaims
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

// NewUserClaims create new user claims
func NewUserClaims() *UserClaims {
	now := gutils.Clock.GetUTCNow()
	return &UserClaims{
		RegisteredClaims: jwtLib.RegisteredClaims{
			ID:        gutils.UUID7(),
			Issuer:    "laisky-blog-graphql",
			IssuedAt:  jwtLib.NewNumericDate(now),
			ExpiresAt: jwtLib.NewNumericDate(now.Add(time.Hour * 24)),
			NotBefore: jwtLib.NewNumericDate(now.Add(-1 * time.Minute)),
		},
	}
}
