package laisky_blog_graphql

import (
	utils "github.com/Laisky/go-utils"
	"github.com/Laisky/laisky-blog-graphql/twitter"
	"github.com/Laisky/zap"
)

var (
	twitterDB *twitter.TwitterDB
)

const (
	TWITTER_DB_NAME  = "twitter"
	TWITTER_COL_NAME = "tweets"
)

func DialDB(addr string) {
	utils.Logger.Info("dial mongodb", zap.String("addr", addr))
	var err error
	if twitterDB, err = twitter.NewTwitterDB(addr, TWITTER_DB_NAME, TWITTER_COL_NAME); err != nil {
		utils.Logger.Panic("connect to db got error", zap.Error(err))
	}
}
