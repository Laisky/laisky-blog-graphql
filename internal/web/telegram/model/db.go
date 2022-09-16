package model

import (
	"context"

	"github.com/Laisky/laisky-blog-graphql/library/db/mongo"
	"github.com/Laisky/laisky-blog-graphql/library/log"

	gconfig "github.com/Laisky/go-config"
	"github.com/Laisky/zap"
)

var (
	MonitorDB mongo.DB
)

func Initialize(ctx context.Context) {

	var err error
	if MonitorDB, err = mongo.NewDB(ctx,
		gconfig.Shared.GetString("settings.db.monitor.addr"),
		gconfig.Shared.GetString("settings.db.monitor.db"),
		gconfig.Shared.GetString("settings.db.monitor.user"),
		gconfig.Shared.GetString("settings.db.monitor.pwd"),
	); err != nil {
		log.Logger.Panic("connect to monitor db", zap.Error(err))
	}
}
