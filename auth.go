package laisky_blog_graphql

import (
	"context"

	"gopkg.in/mgo.v2/bson"

	"github.com/Laisky/laisky-blog-graphql/blog"

	"github.com/pkg/errors"
)

func validateAndGetUser(ctx context.Context) (user *blog.User, err error) {
	var uid bson.ObjectId
	if uid, err = auth.ValidateAndGetUID(ctx); err != nil {
		return nil, errors.Wrap(err, "token invalidate")
	}

	if user, err = blogDB.LoadUserById(uid); err != nil {
		return nil, errors.Wrap(err, "can not validate user")
	}

	return user, nil
}
