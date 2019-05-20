package laisky_blog_graphql

import (
	"context"

	"github.com/pkg/errors"
	"gopkg.in/mgo.v2/bson"

	utils "github.com/Laisky/go-utils"
	"github.com/Laisky/laisky-blog-graphql/blog"
)

const (
	AuthTokenName    = "token"
	AuthUserIdCtxKey = "auth_uid"
)

var Auth = &AuthType{}

type AuthType struct {
	utils.JWT
}

func SetupAuth(secret string) {
	Auth.Setup(secret)
}

func ValidateAndGetUser(ctx context.Context) (user *blog.User, err error) {
	token := getIrisCtxFromStdCtx(ctx).GetCookie(AuthTokenName)
	payload, err := Auth.Validate(token)
	if err != nil {
		return nil, errors.Wrap(err, "token invalidate")
	}

	uid := bson.ObjectIdHex(payload[Auth.TKUsername].(string))
	if user, err = blogDB.LoadUserById(uid); err != nil {
		return nil, errors.Wrap(err, "can not validate user")
	}

	return user, nil
}
