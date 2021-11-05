package dao

import (
	"context"

	"laisky-blog-graphql/internal/web/twitter/model"
)

var (
	InstanceTweets *Tweets
	InstanceSearch *Search
)

func Initialize(ctx context.Context) {
	model.Initialize(ctx)

	InstanceTweets = NewTweets(model.TwitterDB)
	InstanceSearch = NewSearch(model.SearchDB)
}
