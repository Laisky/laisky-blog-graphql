package laisky_blog_graphql

import (
	"context"
	"path/filepath"

	utils "github.com/Laisky/go-utils"
	"github.com/Laisky/laisky-blog-graphql/blog"
	"github.com/Laisky/laisky-blog-graphql/general"
	"github.com/Laisky/laisky-blog-graphql/libs"
	"github.com/Laisky/laisky-blog-graphql/models"
	"github.com/Laisky/laisky-blog-graphql/telegram"
	"github.com/Laisky/laisky-blog-graphql/twitter"
	"github.com/Laisky/zap"
	"google.golang.org/api/option"
)

var (
	twitterDB *twitter.TwitterDB
	blogDB    *blog.BlogDB
	monitorDB *telegram.MonitorDB
	generalDB *general.GeneralDB
)

func setupDB(ctx context.Context) {
	setupMongo(ctx)
	setupGCP(ctx)
}

func setupGCP(ctx context.Context) {
	defer libs.Logger.Info("connected gcp firestore")
	generalFirestore, err := models.NewFirestore(
		ctx,
		utils.Settings.GetString("settings.general.project_id"),
		option.WithCredentialsFile(filepath.Join(
			utils.Settings.GetString("cfg_dir"),
			utils.Settings.GetString("settings.general.credential_file"),
		)),
	)
	if err != nil {
		libs.Logger.Panic("create firestore client", zap.Error(err))
	}
	generalDB = general.NewGeneralDB(generalFirestore)
}

func setupMongo(ctx context.Context) {
	defer libs.Logger.Info("connected mongodb")
	var (
		blogDBCli,
		twitterDBCli,
		monitorDBCli *models.DB
		err error
	)
	if blogDBCli, err = models.NewMongoDB(ctx,
		utils.Settings.GetString("settings.db.blog.addr"),
		utils.Settings.GetString("settings.db.blog.db"),
		utils.Settings.GetString("settings.db.blog.user"),
		utils.Settings.GetString("settings.db.blog.pwd"),
	); err != nil {
		libs.Logger.Panic("connect to blog db", zap.Error(err))
	}
	blogDB = blog.NewBlogDB(blogDBCli)

	if twitterDBCli, err = models.NewMongoDB(ctx,
		utils.Settings.GetString("settings.db.twitter.addr"),
		utils.Settings.GetString("settings.db.twitter.db"),
		utils.Settings.GetString("settings.db.twitter.user"),
		utils.Settings.GetString("settings.db.twitter.pwd"),
	); err != nil {
		libs.Logger.Panic("connect to twitter db", zap.Error(err))
	}
	twitterDB = twitter.NewTwitterDB(twitterDBCli)

	if monitorDBCli, err = models.NewMongoDB(ctx,
		utils.Settings.GetString("settings.db.monitor.addr"),
		utils.Settings.GetString("settings.db.monitor.db"),
		utils.Settings.GetString("settings.db.monitor.user"),
		utils.Settings.GetString("settings.db.monitor.pwd"),
	); err != nil {
		libs.Logger.Panic("connect to monitor db", zap.Error(err))
	}
	monitorDB = telegram.NewMonitorDB(monitorDBCli)
}
