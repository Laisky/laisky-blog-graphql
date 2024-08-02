package model

import (
	"context"

	"github.com/Laisky/errors/v2"
	gconfig "github.com/Laisky/go-config/v2"

	"github.com/Laisky/laisky-blog-graphql/library/db/mongo"
)

// NewMonitorDB create new monitor db
func NewMonitorDB(ctx context.Context) (db mongo.DB, err error) {
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

// NewTelegramDB create new telegram db
func NewTelegramDB(ctx context.Context) (db mongo.DB, err error) {
	db, err = mongo.NewDB(ctx,
		mongo.DialInfo{
			Addr:   gconfig.Shared.GetString("settings.db.telegram.addr"),
			DBName: gconfig.Shared.GetString("settings.db.telegram.db"),
			User:   gconfig.Shared.GetString("settings.db.telegram.user"),
			Pwd:    gconfig.Shared.GetString("settings.db.telegram.pwd"),
		},
	)
	if err != nil {
		return nil, errors.Wrap(err, "dial db")
	}

	return db, nil
}
