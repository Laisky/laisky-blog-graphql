package controller

import (
	"context"
	"fmt"
	"strings"

	"laisky-blog-graphql/internal/global"
	"laisky-blog-graphql/internal/web/twitter/dto"
	"laisky-blog-graphql/internal/web/twitter/model"
	"laisky-blog-graphql/internal/web/twitter/service"
	"laisky-blog-graphql/library"
	"laisky-blog-graphql/library/log"

	"github.com/Laisky/zap"
	"github.com/pkg/errors"
)

type TweetResolver struct{}
type TwitterUserResolver struct{}

type QueryResolver struct{}

type Type struct {
	TweetResolver       *TweetResolver
	TwitterUserResolver *TwitterUserResolver
}

func New() *Type {
	return &Type{
		TweetResolver:       new(TweetResolver),
		TwitterUserResolver: new(TwitterUserResolver),
	}
}

var Instance *Type

func Initialize(ctx context.Context) {
	service.Initialize(ctx)

	Instance = New()
}

// =================
// query resolver
// =================

func (r *QueryResolver) TwitterStatues(ctx context.Context,
	page *global.Pagination,
	tweetID string,
	username string,
	viewerID string,
	sort *global.Sort,
	topic string,
	regexp string,
) (results []*model.Tweet, err error) {
	args := &dto.LoadTweetArgs{
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

	if results, err = service.Instance.LoadTweets(args); err != nil {
		return nil, err
	}

	return results, nil
}

func (r *QueryResolver) TwitterThreads(ctx context.Context, tweetID string) ([]*model.Tweet, error) {
	return service.Instance.LoadThreadByTweetID(tweetID)
}

// ----------------
// twitter resolver
// ----------------

// func (t *TwitterUserResolver) ID(ctx context.Context, obj *model.User) (string, error) {
// 	return obj.ID, nil
// }

func (t *TwitterUserResolver) Description(ctx context.Context, obj *model.User) (string, error) {
	return obj.Dscription, nil
}

func (t *TweetResolver) ID(ctx context.Context, obj *model.Tweet) (string, error) {
	return obj.ID, nil
}

func (t *TweetResolver) IsQuoteStatus(ctx context.Context, obj *model.Tweet) (bool, error) {
	return obj.IsQuoted, nil
}

func (t *TweetResolver) Replys(ctx context.Context, obj *model.Tweet) ([]*model.Tweet, error) {
	return service.Instance.LoadTweetReplys(obj.ID)
}

func (t *TweetResolver) Images(ctx context.Context, obj *model.Tweet) (imgs []string, err error) {
	if obj.Entities == nil {
		return
	}

	for _, e := range obj.Entities.Media {
		us := strings.Split(e.URL, "/")
		imgs = append(imgs, ("https://s3.laisky.com/uploads/twitter/" + us[len(us)-1]))
	}

	return
}

func (t *TweetResolver) Viewers(ctx context.Context, obj *model.Tweet) (us []*model.User, err error) {
	for _, uid := range obj.Viewer {
		u, err := service.Instance.LoadUserByID(uid)
		if err != nil {
			return nil, errors.WithStack(err)
		}

		us = append(us, u)
	}

	return us, nil
}

func (t *TweetResolver) QuotedStatus(ctx context.Context, obj *model.Tweet) (*model.Tweet, error) {
	return obj.QuotedTweet, nil
}

// func (t *TweetResolver) MongoID(ctx context.Context, obj *model.Tweet) (string, error) {
// 	return obj.MongoID.Hex(), nil
// }

// func (t *TweetResolver) TweetID(ctx context.Context, obj *model.Tweet) (int, error) {
// 	return int(obj.ID), nil
// }

func (t *TweetResolver) CreatedAt(ctx context.Context, obj *model.Tweet) (*library.Datetime, error) {
	if obj.CreatedAt == nil {
		return nil, nil
	}

	return library.NewDatetimeFromTime(*obj.CreatedAt), nil
}

func (t *TweetResolver) URL(ctx context.Context, obj *model.Tweet) (string, error) {
	if obj.User == nil {
		return "", nil
	}

	return "https://twitter.com/" + obj.User.ScreenName + "/status/" + obj.ID, nil
}

func (t *TweetResolver) ReplyTo(ctx context.Context, obj *model.Tweet) (tweet *model.Tweet, err error) {
	if obj.ReplyToStatusID == "" {
		return nil, nil
	}

	if tweet, err = service.Instance.LoadTweetByTwitterID(obj.ReplyToStatusID); err != nil {
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
