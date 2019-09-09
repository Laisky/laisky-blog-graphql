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

func DialDB(ctx context.Context, addr string) {
	utils.Logger.Info("dial mongodb", zap.String("addr", addr))
	var (
		dbcli *models.DB
		err   error
	)
	if dbcli, err = models.NewMongoDB(ctx, addr); err != nil {
		utils.Logger.Panic("connect to db got error", zap.Error(err))
	}

	twitterDB = twitter.NewTwitterDB(dbcli)
	blogDB = blog.NewBlogDB(dbcli)
}
