package model

import (
	"context"

	"github.com/Laisky/errors/v2"
	"github.com/Laisky/laisky-blog-graphql/library/db/mongo"

	gconfig "github.com/Laisky/go-config/v2"
)

func New(ctx context.Context) (db mongo.DB, err error) {
	db, err = mongo.NewDB(ctx,
		mongo.DialInfo{
			Addr:   gconfig.Shared.GetString("settings.db.monitor.addr"),
			DBName: gconfig.Shared.GetString("settings.db.monitor.db"),
			User:   gconfig.Shared.GetString("settings.db.monitor.user"),
			Pwd:    gconfig.Shared.GetString("settings.db.monitor.pwd"),
		},
	)
	if err != nil {
		return nil, errors.Wrap(err, "dial db")
	}

	return db, nil
}
