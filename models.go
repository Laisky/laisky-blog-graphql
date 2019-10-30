package laisky_blog_graphql

import (
	"context"

	"github.com/Laisky/laisky-blog-graphql/models"

	"github.com/Laisky/laisky-blog-graphql/telegram"

	utils "github.com/Laisky/go-utils"
	"github.com/Laisky/laisky-blog-graphql/blog"
	"github.com/Laisky/laisky-blog-graphql/twitter"
	"github.com/Laisky/zap"
)

var (
	twitterDB *twitter.TwitterDB
	blogDB    *blog.BlogDB
	monitorDB *telegram.MonitorDB
)

func setupDB(ctx context.Context) {
	utils.Logger.Info("dial mongodb")
	var (
		blogDBCli, twitterDBCli, monitorDBCli *models.DB
		err                                   error
	)
	if blogDBCli, err = models.NewMongoDB(ctx,
		utils.Settings.GetString("settings.db.blog.addr"),
		utils.Settings.GetString("settings.db.blog.db"),
		utils.Settings.GetString("settings.db.blog.user"),
		utils.Settings.GetString("settings.db.blog.pwd"),
	); err != nil {
		utils.Logger.Panic("connect to blog db", zap.Error(err))
	}
	blogDB = blog.NewBlogDB(blogDBCli)

	if twitterDBCli, err = models.NewMongoDB(ctx,
		utils.Settings.GetString("settings.db.twitter.addr"),
		utils.Settings.GetString("settings.db.twitter.db"),
		utils.Settings.GetString("settings.db.twitter.user"),
		utils.Settings.GetString("settings.db.twitter.pwd"),
	); err != nil {
		utils.Logger.Panic("connect to twitter db", zap.Error(err))
	}
	twitterDB = twitter.NewTwitterDB(twitterDBCli)

	if monitorDBCli, err = models.NewMongoDB(ctx,
		utils.Settings.GetString("settings.db.monitor.addr"),
		utils.Settings.GetString("settings.db.monitor.db"),
		utils.Settings.GetString("settings.db.monitor.user"),
		utils.Settings.GetString("settings.db.monitor.pwd"),
	); err != nil {
		utils.Logger.Panic("connect to monitor db", zap.Error(err))
	}
	monitorDB = telegram.NewMonitorDB(monitorDBCli)
}
