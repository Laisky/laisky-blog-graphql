package web

import (
	"context"
	"fmt"
	"strings"

	"laisky-blog-graphql/internal/global"
	"laisky-blog-graphql/internal/web/twitter"
	"laisky-blog-graphql/library"
	"laisky-blog-graphql/library/log"

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
	tweetID string,
	username string,
	viewerID string,
	sort *Sort,
	topic string,
	regexp string,
) (results []*twitter.Tweet, err error) {
	args := &twitter.LoadTweetArgs{
		SortBy:    sort.SortBy,
		SortOrder: string(sort.Order),
	}
	switch {
	case tweetID != "":
		args.TweetID = tweetID
	default:
		args.Page = page.Page
		args.Regexp = regexp
		args.Size = page.Size
		args.Username = username
		args.ViewerID = viewerID
		args.TweetID = tweetID
	}

	if results, err = global.TwitterSvc.LoadTweets(args); err != nil {
		return nil, err
	}

	return results, nil
}

func (q *queryResolver) TwitterThreads(ctx context.Context, tweetID string) ([]*twitter.Tweet, error) {
	return global.TwitterSvc.LoadThreadByTweetID(tweetID)
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
	return global.TwitterSvc.LoadTweetReplys(obj.ID)
}

func (t *tweetResolver) Images(ctx context.Context, obj *twitter.Tweet) (imgs []string, err error) {
	if obj.Entities == nil {
		return
	}

	for _, e := range obj.Entities.Media {
		us := strings.Split(e.URL, "/")
		imgs = append(imgs, ("https://s3.laisky.com/uploads/twitter/" + us[len(us)-1]))
	}

	return
}

func (t *tweetResolver) Viewers(ctx context.Context, obj *twitter.Tweet) (us []*twitter.User, err error) {
	for _, uid := range obj.Viewer {
		u, err := global.TwitterSvc.LoadUserByID(uid)
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

func (t *tweetResolver) CreatedAt(ctx context.Context, obj *twitter.Tweet) (*library.Datetime, error) {
	if obj.CreatedAt == nil {
		return nil, nil
	}

	return library.NewDatetimeFromTime(*obj.CreatedAt), nil
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

	if tweet, err = global.TwitterSvc.LoadTweetByTwitterID(obj.ReplyToStatusID); err != nil {
		log.Logger.Warn("try to load tweet by id got error",
			zap.String("tweet", obj.ReplyToStatusID),
			zap.Error(err))
		return nil, fmt.Errorf("can not load tweet by tid: %v", obj.ReplyToStatusID)
	}

	return tweet, nil
}

// ============================
// mutations
// ============================
