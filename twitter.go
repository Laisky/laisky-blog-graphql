package laisky_blog_graphql

import (
	"context"
	"fmt"
	"strings"

	"github.com/Laisky/laisky-blog-graphql/libs"
	"github.com/Laisky/laisky-blog-graphql/twitter"
	"github.com/Laisky/zap"
	"github.com/pkg/errors"
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

func (q *queryResolver) TwitterStatues(ctx context.Context,
	page *Pagination,
	username string,
	viewerID string,
	sort *Sort,
	topic string,
	regexp string,
) (results []*twitter.Tweet, err error) {
	if results, err = twitterDB.LoadTweets(&twitter.TweetLoadCfg{
		Page:      page.Page,
		Regexp:    regexp,
		Size:      page.Size,
		Username:  username,
		ViewerID:  viewerID,
		SortBy:    sort.SortBy,
		SortOrder: string(sort.Order),
	}); err != nil {
		return nil, err
	}

	return results, nil
}

func (q *queryResolver) TwitterThreads(ctx context.Context, tweetID string) ([]*twitter.Tweet, error) {
	return twitterDB.LoadThreadByTweetID(tweetID)
}

// ----------------
// twitter resolver
// ----------------

// func (t *twitterUserResolver) ID(ctx context.Context, obj *twitter.User) (string, error) {
// 	return obj.ID, nil
// }

func (t *twitterUserResolver) Description(ctx context.Context, obj *twitter.User) (string, error) {
	return obj.Dscription, nil
}

func (t *tweetResolver) ID(ctx context.Context, obj *twitter.Tweet) (string, error) {
	return obj.ID, nil
}

func (t *tweetResolver) IsQuoteStatus(ctx context.Context, obj *twitter.Tweet) (bool, error) {
	return obj.IsQuoted, nil
}

func (t *tweetResolver) Replys(ctx context.Context, obj *twitter.Tweet) ([]*twitter.Tweet, error) {
	return twitterDB.LoadTweetReplys(obj.ID)
}

func (t *tweetResolver) Images(ctx context.Context, obj *twitter.Tweet) (imgs []string, err error) {
	for _, e := range obj.Entities.Media {
		us := strings.Split(e.URL, "/")
		imgs = append(imgs, ("https://s3.laisky.com/uploads/twitter/" + us[len(us)-1]))
	}

	return
}

func (t *tweetResolver) Viewers(ctx context.Context, obj *twitter.Tweet) (us []*twitter.User, err error) {
	for _, uid := range obj.Viewer {
		u, err := twitterDB.LoadUserByID(uid)
		if err != nil {
			return nil, errors.WithStack(err)
		}

		us = append(us, u)
	}

	return us, nil
}

func (t *tweetResolver) QuotedStatus(ctx context.Context, obj *twitter.Tweet) (*twitter.Tweet, error) {
	return obj.QuotedTweet, nil
}

// func (t *tweetResolver) MongoID(ctx context.Context, obj *twitter.Tweet) (string, error) {
// 	return obj.MongoID.Hex(), nil
// }

// func (t *tweetResolver) TweetID(ctx context.Context, obj *twitter.Tweet) (int, error) {
// 	return int(obj.ID), nil
// }

func (t *tweetResolver) CreatedAt(ctx context.Context, obj *twitter.Tweet) (*libs.Datetime, error) {
	if obj.CreatedAt == nil {
		return nil, nil
	}

	return libs.NewDatetimeFromTime(*obj.CreatedAt), nil
}

func (t *tweetResolver) URL(ctx context.Context, obj *twitter.Tweet) (string, error) {
	if obj.User == nil {
		return "", nil
	}
	return "https://twitter.com/" + obj.User.ScreenName + "/status/" + obj.ID, nil
}

func (t *tweetResolver) ReplyTo(ctx context.Context, obj *twitter.Tweet) (tweet *twitter.Tweet, err error) {
	if obj.ReplyToStatusID == "" {
		return nil, nil
	}

	if tweet, err = twitterDB.LoadTweetByTwitterID(obj.ReplyToStatusID); err != nil {
		libs.Logger.Warn("try to load tweet by id got error",
			zap.String("tweet", obj.ReplyToStatusID),
			zap.Error(err))
		return nil, fmt.Errorf("can not load tweet by tid: %v", obj.ReplyToStatusID)
	}

	return tweet, nil
}

// ============================
// mutations
// ============================
