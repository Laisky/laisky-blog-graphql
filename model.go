package laisky_blog_graphql

import (
	utils "github.com/Laisky/go-utils"
	"github.com/Laisky/laisky-blog-graphql/blog"
	"github.com/Laisky/laisky-blog-graphql/twitter"
	"github.com/Laisky/zap"
)

var (
	twitterDB *twitter.TwitterDB
	blogDB    *blog.BlogDB
)

func DialDB(addr string) {
	utils.Logger.Info("dial mongodb", zap.String("addr", addr))
	var err error
	if twitterDB, err = twitter.NewTwitterDB(addr); err != nil {
		utils.Logger.Panic("connect to db got error", zap.Error(err))
	}
	if blogDB, err = blog.NewBlogDB(addr); err != nil {
		utils.Logger.Panic("connect to db got error", zap.Error(err))
	}
}
