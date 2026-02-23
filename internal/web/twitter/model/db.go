package model

import (
	"context"

	"github.com/Laisky/laisky-blog-graphql/library/db/mongo"
	"github.com/Laisky/laisky-blog-graphql/library/log"

	gconfig "github.com/Laisky/go-config/v2"
	"github.com/Laisky/zap"
)

var (
	TwitterDB mongo.DB
)

func Initialize(ctx context.Context) {
	var err error
	if TwitterDB, err = mongo.NewDB(ctx,
		mongo.DialInfo{
			Addr:   gconfig.Shared.GetString("settings.db.twitter.addr"),
			DBName: gconfig.Shared.GetString("settings.db.twitter.db"),
			User:   gconfig.Shared.GetString("settings.db.twitter.user"),
			Pwd:    gconfig.Shared.GetString("settings.db.twitter.pwd"),
		},
	); err != nil {
		log.Logger.Panic("connect to twitter db", zap.Error(err))
	}

}
