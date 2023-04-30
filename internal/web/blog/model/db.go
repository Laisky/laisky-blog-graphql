package model

import (
	"context"

	"github.com/Laisky/laisky-blog-graphql/library/db/mongo"
	"github.com/Laisky/laisky-blog-graphql/library/log"

	gconfig "github.com/Laisky/go-config/v2"
	"github.com/Laisky/zap"
)

var (
	BlogDB mongo.DB
)

func Initialize(ctx context.Context) {
	var err error
	if BlogDB, err = mongo.NewDB(ctx,
		mongo.DialInfo{
			Addr:   gconfig.Shared.GetString("settings.db.blog.addr"),
			DBName: gconfig.Shared.GetString("settings.db.blog.db"),
			User:   gconfig.Shared.GetString("settings.db.blog.user"),
			Pwd:    gconfig.Shared.GetString("settings.db.blog.pwd"),
		},
	); err != nil {
		log.Logger.Panic("connect to blog db", zap.Error(err))
	}
}
