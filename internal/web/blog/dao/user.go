package dao

import (
	"context"
	"fmt"

	"github.com/Laisky/errors/v2"
	gcrypto "github.com/Laisky/go-utils/v4/crypto"
	"github.com/Laisky/laisky-blog-graphql/internal/web/blog/model"
	"github.com/Laisky/zap"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

var ErrLogin = errors.New("Password Or Username Incorrect")

// ValidateLogin validate user login
func (d *Blog) ValidateLogin(ctx context.Context, account, password string) (u *model.User, err error) {
	d.logger.Debug("ValidateLogin", zap.String("account", account))
	u = &model.User{}
	if err := d.GetUsersCol().
		FindOne(ctx, bson.D{{Key: "account", Value: account}}).
		Decode(u); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, fmt.Errorf("user notfound")
		}

		return nil, err
	}

	if gcrypto.ValidatePasswordHash([]byte(u.Password), []byte(password)) {
		d.logger.Debug("user login", zap.String("user", u.Account))
		return u, nil
	}

	return nil, ErrLogin
}
