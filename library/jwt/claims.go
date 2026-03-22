package jwt

import (
	"time"

	gutils "github.com/Laisky/go-utils/v6"
	jwtLib "github.com/golang-jwt/jwt/v5"
)

type UserClaims struct {
	jwtLib.RegisteredClaims
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
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
