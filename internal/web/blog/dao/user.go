package dao

import (
	"context"
	"sync"

	"github.com/Laisky/errors/v2"
	gutils "github.com/Laisky/go-utils/v6"
	gcrypto "github.com/Laisky/go-utils/v6/crypto"
	"github.com/Laisky/zap"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"

	"github.com/Laisky/laisky-blog-graphql/internal/web/blog/model"
)

var (
	dummyPasswordHashOnce sync.Once
	dummyPasswordHash     string
)

// dummyPasswordHash returns a precomputed hash used to mask login timing.
// It accepts no parameters and returns the dummy hash string.
func (d *Blog) dummyPasswordHash() string {
	dummyPasswordHashOnce.Do(func() {
		hash, err := gcrypto.PasswordHash([]byte("invalid-password-placeholder"), gutils.HashTypeSha256)
		if err != nil {
			d.logger.Error("generate dummy password hash", zap.Error(errors.Wrap(err, "generate dummy password hash")))
			return
		}
		dummyPasswordHash = hash
	})

	return dummyPasswordHash
}

// ValidateLogin validates a login attempt with the provided account and password.
// It accepts a context, account string, and raw password, returning the user on success or an error on failure.
func (d *Blog) ValidateLogin(ctx context.Context, account, rawPassword string) (u *model.User, err error) {
	d.logger.Debug("ValidateLogin", zap.String("account", account))
	u = &model.User{}
	if err := d.GetUsersCol().
		FindOne(ctx, bson.D{{Key: "account", Value: account}}).
		Decode(u); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			_ = gcrypto.VerifyHashedPassword([]byte(rawPassword), d.dummyPasswordHash())
			return nil, errors.WithStack(model.ErrInvalidCredentials)
		}
		return nil, errors.Wrap(err, "find user")
	}

	if err = gcrypto.VerifyHashedPassword([]byte(rawPassword), u.Password); err != nil {
		return nil, errors.WithStack(model.ErrInvalidCredentials)
	}

	return u, nil
}
