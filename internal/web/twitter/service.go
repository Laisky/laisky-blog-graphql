package twitter

import (
	"strconv"

	"laisky-blog-graphql/internal/web/twitter/db"
	"laisky-blog-graphql/library/log"

	"github.com/Laisky/zap"
	"github.com/pkg/errors"
	mgo "gopkg.in/mgo.v2"
	"gopkg.in/mgo.v2/bson"
)

type Service struct {
	*db.DB
}

func NewService(db *db.DB) *Service {
	return &Service{DB: db}
}

func (s *Service) LoadTweetReplys(tweetID string) (replys []*Tweet, err error) {
	if err = s.GetTweetCol().
		Find(bson.M{"in_reply_to_status_id_str": tweetID}).
		All(&replys); err != nil {
		return nil, errors.Wrapf(err, "load replys of tweet `%s`", tweetID)
	}

	return
}

func (s *Service) LoadThreadByTweetID(id string) (tweets []*Tweet, err error) {
	tweet := &Tweet{}
	if err = s.GetTweetCol().
		Find(bson.M{"id_str": id}).
		One(tweet); err != nil {
		return nil, errors.Wrapf(err, "load tweet `%s`", id)
	}

	head, err := s.loadTweetsRecur(tweet, func(status *Tweet) string {
		return status.ReplyToStatusID
	})
	if err != nil {
		return nil, errors.Wrapf(err, "load head for tweet `%s`", id)
	}

	tail, err := s.loadTweetsRecur(tweet, func(status *Tweet) (nextID string) {
		replys, err := s.LoadTweetReplys(status.ID)
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

func (s *Service) loadTweetsRecur(tweet *Tweet, getNextID func(*Tweet) string) (tweets []*Tweet, err error) {
	var nextID string
	for {
		if nextID = getNextID(tweet); nextID == "" {
			break
		}

		tweet = &Tweet{}
		if err = s.GetTweetCol().
			Find(bson.M{"id_str": nextID}).
			One(tweet); err != nil {
			if errors.Is(err, mgo.ErrNotFound) {
				break
			}

			return nil, errors.Wrapf(err, "load tweet `%s`", nextID)
		}

		tweets = append(tweets, tweet)
	}

	return tweets, nil
}

func (s *Service) LoadTweetByTwitterID(id string) (tweet *Tweet, err error) {
	tweet = &Tweet{}
	if err = s.GetTweetCol().
		Find(bson.M{"id_str": id}).
		One(tweet); err == mgo.ErrNotFound {
		log.Logger.Debug("tweet not found", zap.String("id", id))
		return &Tweet{ID: id}, nil
	} else if err != nil {
		return nil, errors.Wrap(err, "try to load tweet by id got error")
	}

	return tweet, nil
}

func (s *Service) LoadUserByID(id string) (user *User, err error) {
	user = new(User)
	if err = s.GetUserCol().
		Find(bson.M{"id_str": id}).
		One(user); err == mgo.ErrNotFound {
		log.Logger.Debug("tweet not found", zap.String("id", id))
		return nil, errors.Errorf("user `%s` not found", id)
	} else if err != nil {
		return nil, errors.Wrap(err, "try to load tweet by id got error")
	}

	return user, nil
}

type LoadTweetArgs struct {
	Page, Size int
	TweetID,
	Topic,
	Regexp,
	Username,
	ViewerID string
	SortBy, SortOrder string
}

func (s *Service) LoadTweets(cfg *LoadTweetArgs) (results []*Tweet, err error) {
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

	results = []*Tweet{}
	var query = bson.M{}
	if cfg.Topic != "" {
		query["topics"] = cfg.Topic
	}

	if cfg.TweetID != "" {
		query["id_str"] = cfg.TweetID
	}

	if cfg.ViewerID != "" {
		vid, err := strconv.Atoi(cfg.ViewerID)
		if err != nil {
			return nil, errors.Wrapf(err, "invalid viewer id `%s`", cfg.ViewerID)
		}

		query["viewer"] = int64(vid)
	}

	if cfg.Regexp != "" {
		query["text"] = bson.M{"$regex": bson.RegEx{
			Pattern: cfg.Regexp,
			Options: "im",
		}}
	}

	if cfg.Username != "" {
		query["user.screen_name"] = cfg.Username
	}

	sort := "-created_at"
	if cfg.SortBy != "" {
		sort = cfg.SortBy
		switch cfg.SortOrder {
		case "ASC":
		case "DESC":
			sort = "-" + sort
		default:
			return nil, errors.Errorf("SortOrder must in `ASC/DESC`, but got %v", cfg.SortOrder)
		}
	}

	if err = s.GetTweetCol().
		Find(query).
		Sort(sort).
		Skip(cfg.Page * cfg.Size).
		Limit(cfg.Size).
		All(&results); err != nil {
		return nil, err
	}

	log.Logger.Debug("load tweets",
		zap.String("sort", sort),
		zap.Any("query", query),
		zap.Int("skip", cfg.Page*cfg.Size),
		zap.Int("size", cfg.Size))
	return results, nil
}
