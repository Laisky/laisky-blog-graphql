// Package service service for twitter API
package service

import (
	"context"
	"strconv"

	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/Laisky/laisky-blog-graphql/internal/web/twitter/dao"
	"github.com/Laisky/laisky-blog-graphql/internal/web/twitter/dto"
	"github.com/Laisky/laisky-blog-graphql/internal/web/twitter/model"
	"github.com/Laisky/laisky-blog-graphql/library/db/mongo"
	"github.com/Laisky/laisky-blog-graphql/library/log"

	"github.com/Laisky/errors/v2"
	"github.com/Laisky/zap"
	"go.mongodb.org/mongo-driver/bson"
)

var Instance *Type

func Initialize(ctx context.Context) {
	dao.Initialize(ctx)

	// Instance = New(dao.InstanceTweets, dao.InstanceSearch)
	Instance = New(dao.InstanceTweets, dao.InstanceTweets)
}

type Type struct {
	tweetDao  *dao.Tweets
	searchDao dao.Search
}

func New(tweet *dao.Tweets, search dao.Search) *Type {
	return &Type{
		tweetDao:  tweet,
		searchDao: search,
	}
}

func (s *Type) LoadTweetReplys(ctx context.Context, tweetID string) (replys []*model.Tweet, err error) {
	cur, err := s.tweetDao.GetTweetCol().
		Find(ctx, bson.M{"in_reply_to_status_id_str": tweetID})
	if err != nil {
		return nil, errors.Wrapf(err, "find replys of tweet `%s`", tweetID)
	}

	if err = cur.All(ctx, &replys); err != nil {
		return nil, errors.Wrapf(err, "load replys of tweet `%s`", tweetID)
	}

	return
}

func (s *Type) LoadThreadByTweetID(ctx context.Context, id string) (tweets []*model.Tweet, err error) {
	tweet := &model.Tweet{}
	if err = s.tweetDao.GetTweetCol().
		FindOne(ctx, bson.M{"id_str": id}).
		Decode(tweet); err != nil {
		return nil, errors.Wrapf(err, "load tweet `%s`", id)
	}

	head, err := s.loadTweetsRecur(ctx, tweet, func(status *model.Tweet) string {
		return status.ReplyToStatusID
	})
	if err != nil {
		return nil, errors.Wrapf(err, "load head for tweet `%s`", id)
	}

	tail, err := s.loadTweetsRecur(ctx, tweet, func(status *model.Tweet) (nextID string) {
		replys, err := s.LoadTweetReplys(ctx, status.ID)
		if err != nil {
			log.Logger.Error("load tweet replies", zap.Error(err))
			return ""
		}

		// load minimal replys (first reply)
		var nextIDInt, nextSelfIDInt int
		for _, s := range replys {
			rid, err := strconv.Atoi(s.ID)
			if err != nil {
				log.Logger.Error("parse tweet id", zap.Error(err), zap.String("id", s.ID))
				return ""
			}

			switch {
			case nextIDInt == 0:
				nextIDInt = rid
				if s.User != nil && tweet.User != nil &&
					s.User.ID == tweet.User.ID {
					nextSelfIDInt = rid
				}
			case rid < nextIDInt:
				nextIDInt = rid
				if s.User != nil && tweet.User != nil &&
					s.User.ID == tweet.User.ID {
					nextSelfIDInt = rid
				}
			}
		}

		// self reply has highest priority
		if nextSelfIDInt != 0 {
			nextIDInt = nextSelfIDInt
		}

		return strconv.Itoa(nextIDInt)
	})
	if err != nil {
		return nil, errors.Wrapf(err, "load tail for tweet `%s`", id)
	}

	for i := len(head) - 1; i >= 0; i-- {
		tweets = append(tweets, head[i])
	}

	tweets = append(tweets, tweet)
	tweets = append(tweets, tail...)
	return tweets, nil
}

func (s *Type) loadTweetsRecur(ctx context.Context,
	tweet *model.Tweet,
	getNextID func(*model.Tweet) string) (
	tweets []*model.Tweet, err error) {
	var nextID string
	for {
		if nextID = getNextID(tweet); nextID == "" {
			break
		}

		tweet = &model.Tweet{}
		if err = s.tweetDao.GetTweetCol().
			FindOne(ctx, bson.M{"id_str": nextID}).
			Decode(tweet); err != nil {
			if mongo.NotFound(err) {
				break
			}

			return nil, errors.Wrapf(err, "load tweet `%s`", nextID)
		}

		tweets = append(tweets, tweet)
	}

	return tweets, nil
}

func (s *Type) LoadTweetByTwitterID(ctx context.Context, id string) (tweet *model.Tweet, err error) {
	tweet = &model.Tweet{}
	if err = s.tweetDao.GetTweetCol().
		FindOne(ctx, bson.M{"id_str": id}).
		Decode(tweet); mongo.NotFound(err) {
		log.Logger.Debug("tweet not found", zap.String("id", id))
		tweet = new(model.Tweet)
		tweet.ID = id
		return tweet, nil
	} else if err != nil {
		return nil, errors.Wrap(err, "try to load tweet by id got error")
	}

	return tweet, nil
}

func (s *Type) LoadUserByID(ctx context.Context, id string) (user *model.User, err error) {
	user = new(model.User)
	if err = s.tweetDao.GetUserCol().
		FindOne(ctx, bson.M{"id_str": id}).
		Decode(user); mongo.NotFound(err) {
		log.Logger.Debug("tweet not found", zap.String("id", id))
		return nil, errors.Errorf("user `%s` not found", id)
	} else if err != nil {
		return nil, errors.Wrap(err, "try to load tweet by id got error")
	}

	return user, nil
}

func (s *Type) LoadTweets(ctx context.Context, cfg *dto.LoadTweetArgs) (results []*model.Tweet, err error) {
	log.Logger.Debug("LoadTweets",
		zap.Int("page", cfg.Page), zap.Int("size", cfg.Size),
		zap.String("topic", cfg.Topic),
		zap.String("tweet_id", cfg.TweetID),
		zap.String("regexp", cfg.Regexp),
		zap.String("sort_by", cfg.SortBy),
		zap.String("viewer", cfg.ViewerID),
		zap.String("sort_order", cfg.SortOrder),
	)
	if cfg.Size > 100 || cfg.Size < 0 {
		return nil, errors.Errorf("size shoule in [0~100]")
	}

	results = []*model.Tweet{}
	var query = bson.D{}
	if cfg.Topic != "" {
		query = append(query, bson.E{Key: "topics", Value: cfg.Topic})
	}

	if cfg.TweetID != "" {
		query = append(query, bson.E{Key: "id_str", Value: cfg.TweetID})
	}

	if cfg.ViewerID != "" {
		vid, err := strconv.Atoi(cfg.ViewerID)
		if err != nil {
			return nil, errors.Wrapf(err, "invalid viewer id `%s`", cfg.ViewerID)
		}

		query = append(query, bson.E{Key: "viewer", Value: int64(vid)})
	}

	if cfg.Regexp != "" {
		// search in clickhouse
		if ids, err := s.searchDao.SearchByText(ctx, cfg.Regexp); err != nil || len(ids) == 0 {
			// clickhouse got error,
			// back to mongo searching
			query = append(query, bson.E{
				Key: "text",
				Value: bson.D{bson.E{Key: "$regex", Value: primitive.Regex{
					Pattern: cfg.Regexp,
					Options: "im",
				}}}})
		} else if cfg.TweetID == "" {
			query = append(query, bson.E{Key: "id_str", Value: bson.M{"$in": ids}})
		}
	}

	if cfg.Username != "" {
		query = append(query, bson.E{Key: "user.screen_name", Value: cfg.Username})
	}

	sort := bson.D{{Key: "-created_at", Value: 1}}
	if cfg.SortBy != "" {
		sort[0].Key = cfg.SortBy
		switch cfg.SortOrder {
		case "ASC":
		case "DESC":
			sort[0].Value = -1
		default:
			return nil, errors.Errorf("SortOrder must in `ASC/DESC`, but got %s", cfg.SortOrder)
		}
	}

	cur, err := s.tweetDao.GetTweetCol().
		Find(ctx, query,
			options.Find().SetSort(sort),
			options.Find().SetSkip(int64(cfg.Page*cfg.Size)),
			options.Find().SetLimit(int64(cfg.Size)),
		)
	if err != nil {
		return nil, errors.Wrap(err, "find tweets")
	}

	if err = cur.All(ctx, &results); err != nil {
		return nil, errors.Wrap(err, "load tweets")
	}

	log.Logger.Debug("load tweets",
		zap.Any("sort", sort),
		zap.Any("query", query),
		zap.Int("skip", cfg.Page*cfg.Size),
		zap.Int("size", cfg.Size))
	return results, nil
}
