package laisky_blog_graphql

import (
	"context"

	utils "github.com/Laisky/go-utils"
	"github.com/Laisky/laisky-blog-graphql/blog"
	"github.com/Laisky/laisky-blog-graphql/models"
	"github.com/Laisky/laisky-blog-graphql/twitter"
	"github.com/Laisky/zap"
)

var (
	twitterDB *twitter.TwitterDB
	blogDB    *blog.BlogDB
)

func DialDB(ctx context.Context) {
	utils.Logger.Info("dial mongodb", zap.String("addr", addr))
	var (
		blogDBCli, twitterDBCli *models.DB
		err                     error
	)
	if blogDBCli, err = models.NewMongoDB(ctx,
		utils.Settings.GetString("settings.db.blog.addr"),
		utils.Settings.GetString("settings.db.blog.db"),
		utils.Settings.GetString("settings.db.blog.user"),
		utils.Settings.GetString("settings.db.blog.pwd"),
	); err != nil {
		utils.Logger.Panic("connect to blog db got error", zap.Error(err))
	}
	blogDB = blog.NewBlogDB(blogDBCli)

	if twitterDBCli, err = models.NewMongoDB(ctx,
		utils.Settings.GetString("settings.db.twitter.addr"),
		utils.Settings.GetString("settings.db.twitter.db"),
		utils.Settings.GetString("settings.db.twitter.user"),
		utils.Settings.GetString("settings.db.twitter.pwd"),
	); err != nil {
		utils.Logger.Panic("connect to twitter db got error", zap.Error(err))
	}
	twitterDB = twitter.NewTwitterDB(twitterDBCli)
}
