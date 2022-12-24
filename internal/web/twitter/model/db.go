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
	// SearchDB  *gorm.DB
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

	// logger := gormLogger.New(stdLog.New(os.Stdout, "\r\n", stdLog.LstdFlags), gormLogger.Config{
	// 	SlowThreshold:             200 * time.Millisecond,
	// 	LogLevel:                  gormLogger.Info,
	// 	IgnoreRecordNotFoundError: false,
	// 	Colorful:                  true,
	// })

	// if SearchDB, err = gorm.Open(
	// 	clickhouse.New(clickhouse.Config{
	// 		DSN:                    gconfig.Shared.GetString("settings.db.clickhouse.dsn"),
	// 		DefaultTableEngineOpts: "ENGINE=Log()",
	// 	}),
	// 	&gorm.Config{
	// 		Logger: logger,
	// 	},
	// ); err != nil {
	// 	log.Logger.Panic("connect to clickhouse", zap.Error(err))
	// }
}
