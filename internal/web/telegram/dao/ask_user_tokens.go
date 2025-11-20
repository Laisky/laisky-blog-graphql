package dao

import (
	"context"

	"github.com/Laisky/errors/v2"
	"github.com/Laisky/go-utils/v6"
	"github.com/Laisky/zap"
	"go.mongodb.org/mongo-driver/bson"
	mongoLib "go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/Laisky/laisky-blog-graphql/library/db/mongo"
	"github.com/Laisky/laisky-blog-graphql/library/log"
)

const askUserTokensColName = "ask_user_tokens"

// AskUserToken provides persistence for ask_user token subscriptions.
type AskUserToken struct {
	db mongo.DB
}

// NewAskUserToken returns an AskUserToken DAO backed by the provided database.
func NewAskUserToken(db mongo.DB) *AskUserToken {
	return &AskUserToken{db: db}
}

func (d *AskUserToken) col() *mongoLib.Collection {
	return d.db.GetCol(askUserTokensColName)
}

// RegisterAskUserToken associates a hashed API key with a Telegram UID.
func (d *AskUserToken) RegisterAskUserToken(ctx context.Context, uid int, tokenHash string) error {
	log.Logger.Info("RegisterAskUserToken", zap.Int("uid", uid))
	_, err := d.col().UpdateOne(ctx,
		bson.M{"token_hash": tokenHash},
		bson.M{
			"$set": bson.M{
				"telegram_uid": uid,
				"modified_at":  utils.Clock.GetUTCNow(),
			},
			"$setOnInsert": bson.M{
				"created_at": utils.Clock.GetUTCNow(),
			},
		},
		options.Update().SetUpsert(true),
	)
	return errors.Wrap(err, "upsert ask_user token")
}

// GetTelegramUIDByTokenHash resolves the Telegram UID for the provided hashed API key.
func (d *AskUserToken) GetTelegramUIDByTokenHash(ctx context.Context, tokenHash string) (int, error) {
	var result struct {
		TelegramUID int `bson:"telegram_uid"`
	}
	if err := d.col().FindOne(ctx, bson.M{"token_hash": tokenHash}).Decode(&result); err != nil {
		return 0, errors.Wrap(err, "find telegram uid by token hash")
	}
	return result.TelegramUID, nil
}
