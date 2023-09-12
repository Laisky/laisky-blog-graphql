package model

import (
	"context"

	gconfig "github.com/Laisky/go-config/v2"

	"github.com/Laisky/laisky-blog-graphql/library/db/mongo"
)

func NewDB(ctx context.Context) (mongo.DB, error) {
	return mongo.NewDB(ctx,
		mongo.DialInfo{
			Addr:   gconfig.Shared.GetString("settings.db.blog.addr"),
			DBName: gconfig.Shared.GetString("settings.db.blog.db"),
			User:   gconfig.Shared.GetString("settings.db.blog.user"),
			Pwd:    gconfig.Shared.GetString("settings.db.blog.pwd"),
		},
	)
}
