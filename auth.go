package laisky_blog_graphql

import (
	"context"
	"time"

	"github.com/kataras/iris"

	irisMiddlewares "github.com/Laisky/go-utils/iris-middlewares"

	"github.com/Laisky/zap"

	"github.com/pkg/errors"
	"gopkg.in/mgo.v2/bson"

	utils "github.com/Laisky/go-utils"
	"github.com/Laisky/laisky-blog-graphql/blog"
)

const (
	AuthTokenName = "token"
)

var Auth = &AuthType{}

type AuthType struct {
	utils.JWT
}

func SetupAuth(secret string) {
	Auth.Setup(secret)
}

func validateAndGetUser(ctx context.Context) (user *blog.User, err error) {
	token := irisMiddlewares.GetIrisCtxFromStdCtx(ctx).GetCookie(AuthTokenName)
	payload, err := Auth.Validate(token)
	if err != nil {
		return nil, errors.Wrap(err, "token invalidate")
	}

	uid := bson.ObjectIdHex(payload[Auth.UserIDKey].(string))
	if user, err = blogDB.LoadUserById(uid); err != nil {
		return nil, errors.Wrap(err, "can not validate user")
	}

	return user, nil
}

const tokenCookieDuration = 7 * 24 * time.Hour

func setLoginCookie(ctx context.Context, user *blog.User) (err error) {
	utils.Logger.Info("user login", zap.String("user", user.Account))
	ctx2 := irisMiddlewares.GetIrisCtxFromStdCtx(ctx)
	payload := map[string]interface{}{
		"display_name": user.Username,
		"account":      user.Account,
	}
	var token string
	if token, err = Auth.GenerateToken(user.ID.Hex(), time.Now().Add(tokenCookieDuration), payload); err != nil {
		return errors.Wrap(err, "try to generate token got error")
	}

	ctx2.SetCookieKV(AuthTokenName, token, iris.CookieExpires(tokenCookieDuration))
	return nil
}
