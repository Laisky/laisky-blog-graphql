package dao

import (
	"context"

	"github.com/Laisky/errors/v2"
	gcrypto "github.com/Laisky/go-utils/v5/crypto"
	"github.com/Laisky/zap"
	"go.mongodb.org/mongo-driver/bson"

	"github.com/Laisky/laisky-blog-graphql/internal/web/blog/model"
)

// ValidateLogin validate user login
func (d *Blog) ValidateLogin(ctx context.Context, account, rawPassword string) (u *model.User, err error) {
	d.logger.Debug("ValidateLogin", zap.String("account", account))
	u = &model.User{}
	if err := d.GetUsersCol().
		FindOne(ctx, bson.D{{Key: "account", Value: account}}).
		Decode(u); err != nil {
		return nil, errors.Wrapf(err, "find user %q", account)
	}

	if err = gcrypto.VerifyHashedPassword([]byte(rawPassword), u.Password); err != nil {
		return nil, errors.Wrapf(err, "verify password for %q", account)
	}

	return u, nil
}
