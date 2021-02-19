package laisky_blog_graphql

import (
	"context"

	"github.com/Laisky/go-utils"
	"github.com/Laisky/laisky-blog-graphql/blog"
	"github.com/pkg/errors"
	"gopkg.in/mgo.v2/bson"
)

var (
	jwtLib *utils.JWT
)

func SetupJWT(secret []byte) (err error) {
	if jwtLib, err = utils.NewJWT(
		utils.WithJWTSecretByte(secret),
		utils.WithJWTSignMethod(utils.SignMethodHS256),
	); err != nil {
		return errors.Wrap(err, "new jwt")
	}
	return nil
}

func validateAndGetUser(ctx context.Context) (user *blog.User, err error) {
	uc := &blog.UserClaims{}
	if err = auth.GetUserClaims(ctx, uc); err != nil {
		return nil, errors.Wrap(err, "get user from token")
	}

	uid := bson.ObjectIdHex(uc.Subject)
	if user, err = blogDB.LoadUserByID(uid); err != nil {
		return nil, errors.Wrapf(err, "load user `%s`", uid)
	}

	return user, nil
}
