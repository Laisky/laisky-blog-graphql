package controller

import (
	"context"
	"strconv"
	"strings"

	"github.com/Laisky/laisky-blog-graphql/internal/library/models"
	"github.com/Laisky/laisky-blog-graphql/internal/web/twitter/dto"
	"github.com/Laisky/laisky-blog-graphql/internal/web/twitter/model"
	"github.com/Laisky/laisky-blog-graphql/internal/web/twitter/service"
	"github.com/Laisky/laisky-blog-graphql/library"
	"github.com/Laisky/laisky-blog-graphql/library/log"

	"github.com/Laisky/errors/v2"
	"github.com/Laisky/zap"
)

type TweetResolver struct{}
type TwitterUserResolver struct{}
type EmbededTweetResolver struct {
	TweetResolver
}

type QueryResolver struct{}

type Type struct {
	TweetResolver        *TweetResolver
	TwitterUserResolver  *TwitterUserResolver
	EmbededTweetResolver *EmbededTweetResolver
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
	page *models.Pagination,
	tweetID string,
	username string,
	viewerID string,
	sort *models.Sort,
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

	if results, err = service.Instance.LoadTweets(ctx, args); err != nil {
		return nil, errors.WithStack(err)
	}

	return results, nil
}

func (r *QueryResolver) TwitterThreads(ctx context.Context, tweetID string) ([]*model.Tweet, error) {
	return service.Instance.LoadThreadByTweetID(ctx, tweetID)
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
	return service.Instance.LoadTweetReplys(ctx, obj.ID)
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
		u, err := service.Instance.LoadUserByID(ctx, strconv.Itoa(int(uid)))
		if err != nil {
			return nil, errors.WithStack(err)
		}

		us = append(us, u)
	}

	return us, nil
}

func (t *TweetResolver) QuotedStatus(ctx context.Context, obj *model.Tweet) (*model.EmbededTweet, error) {
	return obj.QuotedTweet, nil
}

// func (t *TweetResolver) MongoID(ctx context.Context, obj *model.Tweet) (string, error) {
// 	return obj.MongoID.Hex(), nil
// }

// func (t *TweetResolver) TweetID(ctx context.Context, obj *model.Tweet) (int, error) {
// 	return int(obj.ID), nil
// }

func (t *TweetResolver) CreatedAt(ctx context.Context, obj *model.Tweet) (*library.Datetime, error) {
	return library.NewDatetimeFromTime(obj.CreatedAt), nil
}

func (t *TweetResolver) URL(ctx context.Context, obj *model.Tweet) (string, error) {
	if obj.User == nil {
		return "", nil
	}

	return "https://twitter.com/" + obj.User.ScreenName + "/status/" + obj.ID, nil
}

func (t *TweetResolver) ReplyTo(ctx context.Context, obj *model.Tweet) (tweet *model.Tweet, err error) {
	if obj.ReplyToStatusID == "" {
		return tweet, nil
	}

	if tweet, err = service.Instance.LoadTweetByTwitterID(ctx, obj.ReplyToStatusID); err != nil {
		log.Logger.Warn("try to load tweet by id got error",
			zap.String("tweet", obj.ReplyToStatusID),
			zap.Error(err))
		return nil, errors.Errorf("can not load tweet by tid: %v", obj.ReplyToStatusID)
	}

	return tweet, nil
}

func (t *EmbededTweetResolver) Images(ctx context.Context, obj *model.EmbededTweet) ([]string, error) {
	tweet, err := obj.ToTweet()
	if err != nil {
		return nil, errors.Wrap(err, "to tweet")
	}

	return t.TweetResolver.Images(ctx, tweet)
}

func (t *EmbededTweetResolver) IsQuoteStatus(ctx context.Context, obj *model.EmbededTweet) (bool, error) {
	tweet, err := obj.ToTweet()
	if err != nil {
		return false, errors.Wrap(err, "to tweet")
	}

	return t.TweetResolver.IsQuoteStatus(ctx, tweet)
}

func (t *EmbededTweetResolver) QuotedStatus(ctx context.Context, obj *model.EmbededTweet) (*model.EmbededTweet, error) {
	return obj.QuotedTweet, nil
}

func (t *EmbededTweetResolver) ReplyTo(ctx context.Context, obj *model.EmbededTweet) (*model.Tweet, error) {
	tweet, err := obj.ToTweet()
	if err != nil {
		return nil, errors.Wrap(err, "to tweet")
	}

	return t.TweetResolver.ReplyTo(ctx, tweet)
}

func (t *EmbededTweetResolver) Replys(ctx context.Context, obj *model.EmbededTweet) ([]*model.Tweet, error) {
	tweet, err := obj.ToTweet()
	if err != nil {
		return nil, errors.Wrap(err, "to tweet")
	}

	return t.TweetResolver.Replys(ctx, tweet)
}

func (t *EmbededTweetResolver) URL(ctx context.Context, obj *model.EmbededTweet) (string, error) {
	tweet, err := obj.ToTweet()
	if err != nil {
		return "", errors.Wrap(err, "to tweet")
	}

	return t.TweetResolver.URL(ctx, tweet)
}

func (t *EmbededTweetResolver) Viewers(ctx context.Context, obj *model.EmbededTweet) ([]*model.User, error) {
	tweet, err := obj.ToTweet()
	if err != nil {
		return nil, errors.Wrap(err, "to tweet")
	}

	return t.TweetResolver.Viewers(ctx, tweet)
}

// ============================
// mutations
// ============================
