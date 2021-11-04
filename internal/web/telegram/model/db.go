package model

import (
	"context"
	"laisky-blog-graphql/library/db"
	"laisky-blog-graphql/library/log"

	gutils "github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
)

var (
	MonitorDB *db.DB
)

func Initialize(ctx context.Context) {

	var err error
	if MonitorDB, err = db.NewMongoDB(ctx,
		gutils.Settings.GetString("settings.db.monitor.addr"),
		gutils.Settings.GetString("settings.db.monitor.db"),
		gutils.Settings.GetString("settings.db.monitor.user"),
		gutils.Settings.GetString("settings.db.monitor.pwd"),
	); err != nil {
		log.Logger.Panic("connect to monitor db", zap.Error(err))
	}
}
