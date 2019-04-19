package laisky_blog_graphql

import (
	"context"
	"time"

	"github.com/Laisky/laisky-blog-graphql/twitter"
)

// THIS CODE IS A STARTING POINT ONLY. IT WILL NOT BE UPDATED WITH SCHEMA CHANGES.

type Resolver struct{}

func (r *Resolver) Query() QueryResolver {
	return &queryResolver{r}
}

func (r *Resolver) Tweet() TweetResolver {
	return &tweetResolver{r}
}
func (r *Resolver) TwitterUser() TwitterUserResolver {
	return &twitterUserResolver{r}
}

// implement resolver
type mutationResolver struct{ *Resolver }

type queryResolver struct{ *Resolver }
type tweetResolver struct{ *Resolver }
type twitterUserResolver struct{ *Resolver }

func (t *twitterUserResolver) Description(ctx context.Context, obj *twitter.User) (*string, error) {
	return &obj.Dscription, nil
}

func (q *queryResolver) Tweets(ctx context.Context, page *Pagination, topic, regexp string) ([]twitter.Tweet, error) {
	if results, err := twitterDB.LoadTweets(page.Page, page.Size, topic, regexp); err != nil {
		return nil, err
	} else {
		return results, nil
	}
}

func (t *tweetResolver) MongoID(ctx context.Context, obj *twitter.Tweet) (string, error) {
	return obj.ID.Hex(), nil
}
func (t *tweetResolver) TweetID(ctx context.Context, obj *twitter.Tweet) (int, error) {
	return int(obj.TID), nil
}
func (t *tweetResolver) CreatedAt(ctx context.Context, obj *twitter.Tweet) (string, error) {
	return obj.CreatedAt.Format(time.RFC3339Nano), nil
}
func (t *tweetResolver) Topics(ctx context.Context, obj *twitter.Tweet) ([]string, error) {
	return obj.Topics, nil
}
func (t *tweetResolver) User(ctx context.Context, obj *twitter.Tweet) (*twitter.User, error) {
	return obj.User, nil
}
