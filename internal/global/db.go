// Package global global shared variables
package global

import (
	"context"
	"path/filepath"

	"laisky-blog-graphql/library/db"
	"laisky-blog-graphql/library/log"

	gutils "github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
	"google.golang.org/api/option"
)

var (
	MonitorDB *db.DB
	GeneralDB *db.Firestore
)

func SetupDB(ctx context.Context) {
	setupMongo(ctx)
	setupGCP(ctx)
}

func setupGCP(ctx context.Context) {
	defer log.Logger.Info("connected gcp firestore")
	var err error
	if GeneralDB, err = db.NewFirestore(
		ctx,
		gutils.Settings.GetString("settings.general.project_id"),
		option.WithCredentialsFile(filepath.Join(
			gutils.Settings.GetString("cfg_dir"),
			gutils.Settings.GetString("settings.general.credential_file"),
		)),
	); err != nil {
		log.Logger.Panic("create firestore client", zap.Error(err))
	}
}

func setupMongo(ctx context.Context) {
	defer log.Logger.Info("connected mongodb")
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
