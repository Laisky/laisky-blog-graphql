package model

import (
	"context"

	"github.com/Laisky/laisky-blog-graphql/library/db/mongo"
	"github.com/Laisky/laisky-blog-graphql/library/log"

	gconfig "github.com/Laisky/go-config"
	"github.com/Laisky/zap"
)

var (
	BlogDB *mongo.DB
)

func Initialize(ctx context.Context) {
	var err error
	if BlogDB, err = mongo.NewDB(ctx,
		gconfig.Shared.GetString("settings.db.blog.addr"),
		gconfig.Shared.GetString("settings.db.blog.db"),
		gconfig.Shared.GetString("settings.db.blog.user"),
		gconfig.Shared.GetString("settings.db.blog.pwd"),
	); err != nil {
		log.Logger.Panic("connect to blog db", zap.Error(err))
	}
}
