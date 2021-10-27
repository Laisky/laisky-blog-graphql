package model

import (
	"context"

	"laisky-blog-graphql/library/db"
	"laisky-blog-graphql/library/log"

	gutils "github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
)

var (
	BlogDB *db.DB
)

func Initialize(ctx context.Context) {
	var err error
	if BlogDB, err = db.NewMongoDB(ctx,
		gutils.Settings.GetString("settings.db.blog.addr"),
		gutils.Settings.GetString("settings.db.blog.db"),
		gutils.Settings.GetString("settings.db.blog.user"),
		gutils.Settings.GetString("settings.db.blog.pwd"),
	); err != nil {
		log.Logger.Panic("connect to blog db", zap.Error(err))
	}
}
