// Package global global shared variables
package global

import (
	"context"
	"path/filepath"

	blogDB "laisky-blog-graphql/internal/web/blog/db"
	generalDB "laisky-blog-graphql/internal/web/general/db"
	telegramDB "laisky-blog-graphql/internal/web/telegram/db"
	twitterDB "laisky-blog-graphql/internal/web/twitter/db"
	"laisky-blog-graphql/library/db"
	"laisky-blog-graphql/library/log"

	"github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
	"google.golang.org/api/option"
)

var (
	TwitterDB *twitterDB.DB
	BlogDB    *blogDB.DB
	MonitorDB *telegramDB.DB
	GeneralDB *generalDB.DB
)

func SetupDB(ctx context.Context) {
	setupMongo(ctx)
	setupGCP(ctx)
}

func setupGCP(ctx context.Context) {
	defer log.Logger.Info("connected gcp firestore")
	generalFirestore, err := db.NewFirestore(
		ctx,
		utils.Settings.GetString("settings.general.project_id"),
		option.WithCredentialsFile(filepath.Join(
			utils.Settings.GetString("cfg_dir"),
			utils.Settings.GetString("settings.general.credential_file"),
		)),
	)
	if err != nil {
		log.Logger.Panic("create firestore client", zap.Error(err))
	}
	GeneralDB = generalDB.NewDB(generalFirestore)
}

func setupMongo(ctx context.Context) {
	defer log.Logger.Info("connected mongodb")
	var (
		blogDBCli,
		twitterDBCli,
		monitorDBCli *db.DB
		err error
	)
	if blogDBCli, err = db.NewMongoDB(ctx,
		utils.Settings.GetString("settings.db.blog.addr"),
		utils.Settings.GetString("settings.db.blog.db"),
		utils.Settings.GetString("settings.db.blog.user"),
		utils.Settings.GetString("settings.db.blog.pwd"),
	); err != nil {
		log.Logger.Panic("connect to blog db", zap.Error(err))
	}
	BlogDB = blogDB.NewDB(blogDBCli)

	if twitterDBCli, err = db.NewMongoDB(ctx,
		utils.Settings.GetString("settings.db.twitter.addr"),
		utils.Settings.GetString("settings.db.twitter.db"),
		utils.Settings.GetString("settings.db.twitter.user"),
		utils.Settings.GetString("settings.db.twitter.pwd"),
	); err != nil {
		log.Logger.Panic("connect to twitter db", zap.Error(err))
	}
	TwitterDB = twitterDB.NewTwitterDB(twitterDBCli)

	if monitorDBCli, err = db.NewMongoDB(ctx,
		utils.Settings.GetString("settings.db.monitor.addr"),
		utils.Settings.GetString("settings.db.monitor.db"),
		utils.Settings.GetString("settings.db.monitor.user"),
		utils.Settings.GetString("settings.db.monitor.pwd"),
	); err != nil {
		log.Logger.Panic("connect to monitor db", zap.Error(err))
	}
	MonitorDB = telegramDB.NewDB(monitorDBCli)
}
