package service

import (
	"context"
	"strings"

	"github.com/Laisky/errors/v2"
	gutils "github.com/Laisky/go-utils/v4"
	gcrypto "github.com/Laisky/go-utils/v4/crypto"
	"github.com/Laisky/laisky-blog-graphql/internal/web/blog/model"
	"github.com/Laisky/zap"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func (s *Blog) ValidateLogin(ctx context.Context, account, password string) (u *model.User, err error) {
	return s.dao.ValidateLogin(ctx, account, password)
}

func (s *Blog) setupUserCols(ctx context.Context) error {
	col := s.dao.GetUsersCol()

	// create unique index for account
	{
		if _, err := col.Indexes().CreateOne(ctx, mongo.IndexModel{
			Keys: bson.M{
				"account": 1,
			},
			Options: options.Index().SetUnique(true),
		}); err != nil {
			return errors.Wrap(err, "create index for account")
		}
	}

	return nil
}

// UserRegister user register
func (s *Blog) UserRegister(ctx context.Context,
	account, password, displayName string) (u *model.User, err error) {
	account = strings.ToLower(strings.TrimSpace(account))
	password = strings.TrimSpace(password)
	if account == "" || password == "" {
		return nil, errors.New("empty account or password")
	}

	col := s.dao.GetUsersCol()
	user := model.NewUser()
	user.Account = account
	user.Username = displayName

	// check duplicate
	{
		user := new(model.User)
		if err = col.FindOne(ctx, bson.M{"account": account}).Decode(user); err == nil {
			return nil, errors.New("user already exists")
		}
	}

	pwd, err := gcrypto.PasswordHash([]byte(password), gutils.HashTypeSha256)
	if err != nil {
		return nil, errors.Wrapf(err, "generate password hash for %q", account)
	}
	user.Password = pwd
	user.ActiveToken = gutils.UUID1()

	// insert new user
	if _, err = col.InsertOne(ctx, user); err != nil {
		return nil, errors.Wrapf(err, "insert user %q", account)
	}

	s.logger.Info("insert new user", zap.String("account", account))
	return user, nil
}

func (s *Blog) UserActive(ctx context.Context, account, activeToken string) (u *model.User, err error) {
	col := s.dao.GetUsersCol()

	user := new(model.User)
	if err = col.FindOne(ctx, bson.M{"account": account}).Decode(user); err != nil {
		return nil, errors.Wrapf(err, "find user %q", account)
	}

	if user.ActiveToken != activeToken {
		return nil, errors.New("invalid active token")
	}

	user.ModifiedAt = gutils.Clock.GetUTCNow()
	user.Status = model.UserStatusActive
	user.ActiveToken = ""

	// save user
	if _, err = col.UpdateOne(ctx, bson.M{"_id": user.ID}, bson.M{"$set": user}); err != nil {
		return nil, errors.Wrapf(err, "update user %q", account)
	}

	return user, nil
}
