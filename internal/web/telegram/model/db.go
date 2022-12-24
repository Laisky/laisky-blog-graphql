package model

import (
	"context"

	"github.com/Laisky/laisky-blog-graphql/library/db/mongo"
	"github.com/Laisky/laisky-blog-graphql/library/log"

	gconfig "github.com/Laisky/go-config/v2"
	"github.com/Laisky/zap"
)

var (
	MonitorDB mongo.DB
)

func Initialize(ctx context.Context) {

	var err error
	if MonitorDB, err = mongo.NewDB(ctx,
		mongo.DialInfo{
			Addr:   gconfig.Shared.GetString("settings.db.monitor.addr"),
			DBName: gconfig.Shared.GetString("settings.db.monitor.db"),
			User:   gconfig.Shared.GetString("settings.db.monitor.user"),
			Pwd:    gconfig.Shared.GetString("settings.db.monitor.pwd"),
		},
	); err != nil {
		log.Logger.Panic("connect to monitor db", zap.Error(err))
	}
}
