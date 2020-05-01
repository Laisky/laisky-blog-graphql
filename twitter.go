package laisky_blog_graphql

import (
	"context"
	"fmt"
	"strconv"

	"github.com/Laisky/laisky-blog-graphql/log"
	"github.com/Laisky/laisky-blog-graphql/twitter"
	"github.com/Laisky/laisky-blog-graphql/types"
	"github.com/Laisky/zap"
)

func (r *Resolver) Tweet() TweetResolver {
	return &tweetResolver{r}
}
func (r *Resolver) TwitterUser() TwitterUserResolver {
	return &twitterUserResolver{r}
}

type tweetResolver struct{ *Resolver }
type twitterUserResolver struct{ *Resolver }

// =================
// query resolver
// =================

func (q *queryResolver) TwitterStatues(ctx context.Context, page *Pagination, username string, sort *Sort, topic string, regexp string) (results []*twitter.Tweet, err error) {
	if results, err = twitterDB.LoadTweets(&twitter.TweetLoadCfg{
		Page:      page.Page,
		Regexp:    regexp,
		Size:      page.Size,
		Username:  username,
		SortBy:    sort.SortBy,
		SortOrder: string(sort.Order),
	}); err != nil {
		return nil, err
	}

	return results, nil
}

// ----------------
// twitter resolver
// ----------------
func (t *twitterUserResolver) ID(ctx context.Context, obj *twitter.User) (string, error) {
	return strconv.FormatInt(int64(obj.ID), 10), nil
}
func (t *twitterUserResolver) Description(ctx context.Context, obj *twitter.User) (string, error) {
	return obj.Dscription, nil
}

func (t *tweetResolver) ID(ctx context.Context, obj *twitter.Tweet) (string, error) {
	return strconv.FormatInt(obj.ID, 10), nil
}
func (t *tweetResolver) IsQuoteStatus(ctx context.Context, obj *twitter.Tweet) (bool, error) {
	return obj.IsQuoted, nil
}
func (t *tweetResolver) QuotedStatus(ctx context.Context, obj *twitter.Tweet) (*twitter.Tweet, error) {
	return obj.QuotedTweet, nil
}
func (t *tweetResolver) MongoID(ctx context.Context, obj *twitter.Tweet) (string, error) {
	return obj.MongoID.Hex(), nil
}
func (t *tweetResolver) TweetID(ctx context.Context, obj *twitter.Tweet) (int, error) {
	return int(obj.ID), nil
}
func (t *tweetResolver) CreatedAt(ctx context.Context, obj *twitter.Tweet) (*types.Datetime, error) {
	if obj.CreatedAt == nil {
		return nil, nil
	}

	return types.NewDatetimeFromTime(*obj.CreatedAt), nil
}
func (t *tweetResolver) URL(ctx context.Context, obj *twitter.Tweet) (string, error) {
	if obj.User == nil {
		return "", nil
	}
	return "https://twitter.com/" + obj.User.ScreenName + "/status/" + strconv.FormatInt(obj.ID, 10), nil
}
func (t *tweetResolver) ReplyTo(ctx context.Context, obj *twitter.Tweet) (tweet *twitter.Tweet, err error) {
	if obj.ReplyToStatusID == 0 {
		return nil, nil
	}

	if tweet, err = twitterDB.LoadTweetByTwitterID(obj.ReplyToStatusID); err != nil {
		log.GetLog().Warn("try to load tweet by id got error",
			zap.Int64("tid", obj.ReplyToStatusID),
			zap.Error(err))
		return nil, fmt.Errorf("can not load tweet by tid: %v", obj.ReplyToStatusID)
	}

	return tweet, nil
}

// ============================
// mutations
// ============================
