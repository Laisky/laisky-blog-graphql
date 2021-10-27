package model

import (
	"context"
	stdLog "log"
	"os"
	"time"

	"laisky-blog-graphql/library/db"
	"laisky-blog-graphql/library/log"

	gutils "github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
	"gorm.io/driver/clickhouse"
	"gorm.io/gorm"
	gormLogger "gorm.io/gorm/logger"
)

var (
	TwitterDB *db.DB
	SearchDB  *gorm.DB
)

func Initialize(ctx context.Context) {
	var err error
	if TwitterDB, err = db.NewMongoDB(ctx,
		gutils.Settings.GetString("settings.db.twitter.addr"),
		gutils.Settings.GetString("settings.db.twitter.db"),
		gutils.Settings.GetString("settings.db.twitter.user"),
		gutils.Settings.GetString("settings.db.twitter.pwd"),
	); err != nil {
		log.Logger.Panic("connect to twitter db", zap.Error(err))
	}

	logger := gormLogger.New(stdLog.New(os.Stdout, "\r\n", stdLog.LstdFlags), gormLogger.Config{
		SlowThreshold:             200 * time.Millisecond,
		LogLevel:                  gormLogger.Info,
		IgnoreRecordNotFoundError: false,
		Colorful:                  true,
	})

	if SearchDB, err = gorm.Open(
		clickhouse.New(clickhouse.Config{
			DSN:                    gutils.Settings.GetString("settings.db.clickhouse.dsn"),
			DefaultTableEngineOpts: "ENGINE=MergeTree()",
		}),
		&gorm.Config{
			Logger: logger,
		},
	); err != nil {
		log.Logger.Panic("connect to clickhouse", zap.Error(err))
	}
}
