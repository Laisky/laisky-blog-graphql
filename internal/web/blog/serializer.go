package blog

import (
	"fmt"

	"github.com/Laisky/go-utils"
	"github.com/dgrijalva/jwt-go"
	"gopkg.in/mgo.v2/bson"
)

type PostInfo struct {
	Total int `json:"total"`
}

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

type PostCfg struct {
	ID                 bson.ObjectId
	Page, Size, Length int
	Name, Tag, Regexp  string
	CategoryURL        *string
}
