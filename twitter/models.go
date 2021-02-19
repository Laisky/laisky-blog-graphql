package twitter

import (
	"fmt"
	"strconv"
	"time"

	"github.com/Laisky/laisky-blog-graphql/libs"
	"github.com/Laisky/laisky-blog-graphql/models"
	"github.com/Laisky/zap"
	"github.com/pkg/errors"
	mgo "gopkg.in/mgo.v2"
	"gopkg.in/mgo.v2/bson"
)

type TwitterDB struct {
	dbcli *models.DB
}

type Media struct {
	ID  int64  `bson:"id" json:"id"`
	URL string `bson:"media_url_https" json:"media_url_https"`
}

type Entities struct {
	Media []*Media `bson:"media" json:"media"`
}

type Tweet struct {
	MongoID         bson.ObjectId `bson:"_id" json:"mongo_id"`
	ID              int64         `bson:"id" json:"id"`
	CreatedAt       *time.Time    `bson:"created_at" json:"created_at"`
	Text            string        `bson:"text" json:"text"`
	Topics          []string      `bson:"topics" json:"topics"`
	User            *User         `bson:"user" json:"user"`
	ReplyToStatusID int64         `bson:"in_reply_to_status_id" json:"in_reply_to_status_id"`
	Entities        *Entities     `bson:"entities" json:"entities"`
	IsRetweeted     bool          `bson:"retweeted" json:"is_retweeted"`
	RetweetedTweet  *Tweet        `bson:"retweeted_status,omitempty" json:"retweeted_tweet"`
	IsQuoted        bool          `bson:"is_quote_status" json:"is_quote_status"`
	QuotedTweet     *Tweet        `bson:"quoted_status,omitempty" json:"quoted_status"`
	Viewer          []int64       `bson:"viewer,omitempty" json:"viewer"`
}

type User struct {
	ID         int64  `bson:"id" json:"id"`
	ScreenName string `bson:"screen_name" json:"screen_name"`
	Name       string `bson:"name" json:"name"`
	Dscription string `bson:"dscription" json:"dscription"`
}

const (
	DB_NAME   = "twitter"
	colTweets = "tweets"
	colUsers  = "users"
)

func NewTwitterDB(dbcli *models.DB) *TwitterDB {
	return &TwitterDB{
		dbcli: dbcli,
	}
}

func (t *TwitterDB) LoadTweetByTwitterID(id int64) (tweet *Tweet, err error) {
	tweet = &Tweet{}
	if err = t.dbcli.GetCol(colTweets).
		Find(bson.M{"id": id}).
		One(tweet); err == mgo.ErrNotFound {
		libs.Logger.Debug("tweet not found", zap.Int64("id", id))
		return &Tweet{ID: id}, nil
	} else if err != nil {
		return nil, errors.Wrap(err, "try to load tweet by id got error")
	}

	return tweet, nil
}

func (t *TwitterDB) LoadUserByID(id int64) (user *User, err error) {
	user = new(User)
	if err = t.dbcli.GetCol(colUsers).
		Find(bson.M{"id": id}).
		One(user); err == mgo.ErrNotFound {
		libs.Logger.Debug("tweet not found", zap.Int64("id", id))
		return nil, errors.Errorf("user `%d` not found", id)
	} else if err != nil {
		return nil, errors.Wrap(err, "try to load tweet by id got error")
	}

	return user, nil
}

type TweetLoadCfg struct {
	Page, Size                        int
	Topic, Regexp, Username, ViewerID string
	SortBy, SortOrder                 string
}

func (t *TwitterDB) LoadTweets(cfg *TweetLoadCfg) (results []*Tweet, err error) {
	libs.Logger.Debug("LoadTweets",
		zap.Int("page", cfg.Page), zap.Int("size", cfg.Size),
		zap.String("topic", cfg.Topic),
		zap.String("regexp", cfg.Regexp),
		zap.String("sort_by", cfg.SortBy),
		zap.String("viewer", cfg.ViewerID),
		zap.String("sort_order", cfg.SortOrder),
	)
	if cfg.Size > 100 || cfg.Size < 0 {
		return nil, fmt.Errorf("size shoule in [0~100]")
	}

	results = []*Tweet{}
	var query = bson.M{}
	if cfg.Topic != "" {
		query["topics"] = cfg.Topic
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

	sort := "-_id"
	if cfg.SortBy != "" {
		sort = cfg.SortBy
		switch cfg.SortOrder {
		case "ASC":
		case "DESC":
			sort = "-" + sort
		default:
			return nil, fmt.Errorf("SortOrder must in `ASC/DESC`, but got %v", cfg.SortOrder)
		}
	}

	if cfg.Username != "" {
		query["user.screen_name"] = cfg.Username
	}

	if err = t.dbcli.GetCol(colTweets).
		Find(query).
		Sort(sort).
		Skip(cfg.Page * cfg.Size).
		Limit(cfg.Size).
		All(&results); err != nil {
		return nil, err
	}

	libs.Logger.Debug("load tweets",
		zap.String("sort", sort),
		zap.Any("query", query),
		zap.Int("skip", cfg.Page*cfg.Size),
		zap.Int("size", cfg.Size))
	return results, nil
}
