package twitter

import (
	"fmt"
	"time"

	"github.com/pkg/errors"

	"github.com/Laisky/go-utils"
	"github.com/Laisky/laisky-blog-graphql/models"
	"github.com/Laisky/zap"
	mgo "gopkg.in/mgo.v2"
	"gopkg.in/mgo.v2/bson"
)

type TwitterDB struct {
	*models.DB
	tweets *mgo.Collection
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
	ID              int64         `bson:"id" json:"tweet_id"`
	CreatedAt       time.Time     `bson:"created_at" json:"created_at"`
	Text            string        `bson:"text" json:"text"`
	Topics          []string      `bson:"topics" json:"topics"`
	User            *User         `bson:"user" json:"user"`
	ReplyToStatusID int64         `bson:"in_reply_to_status_id" json:"in_reply_to_status_id"`
	Entities        Entities      `bson:"entities" json:"entities"`
	IsRetweeted     bool          `bson:"retweeted" json:"is_retweeted"`
	RetweetedTweet  *Tweet        `bson:"retweeted_status,omitempty" json:"retweeted_tweet,omitempty"`
}

type User struct {
	ID         int32  `bson:"id" json:"id"`
	ScreenName string `bson:"screen_name" json:"screen_name"`
	Name       string `bson:"name" json:"name"`
	Dscription string `bson:"dscription" json:"dscription"`
}

const (
	DB_NAME        = "twitter"
	TWEET_COL_NAME = "tweets"
)

func NewTwitterDB(addr string) (db *TwitterDB, err error) {
	db = &TwitterDB{
		DB: &models.DB{},
	}
	if err = db.Dial(addr, DB_NAME); err != nil {
		return nil, err
	}

	db.tweets = db.GetCol(TWEET_COL_NAME)
	return db, nil
}

func (t *TwitterDB) LoadTweetByTwitterID(id int64) (tweet *Tweet, err error) {
	tweet = &Tweet{}
	if err = t.tweets.Find(bson.M{"id": id}).One(tweet); err == mgo.ErrNotFound {
		utils.Logger.Warn("tweet not found", zap.Int64("id", id))
		return &Tweet{ID: id}, nil
	} else if err != nil {
		return nil, errors.Wrap(err, "try to load tweet by id got error")
	}

	return tweet, nil
}

func (t *TwitterDB) LoadTweets(page, size int, topic, regexp string) (results []*Tweet, err error) {
	utils.Logger.Debug("LoadTweets",
		zap.Int("page", page), zap.Int("size", size),
		zap.String("topic", topic),
		zap.String("regexp", regexp),
	)

	if size > 100 || size < 0 {
		return nil, fmt.Errorf("size shoule in [0~100]")
	}

	results = []*Tweet{}
	var query = bson.M{}
	if topic != "" {
		query["topics"] = topic
	}

	if regexp != "" {
		query["text"] = bson.M{"$regex": bson.RegEx{regexp, "im"}}
	}

	if err = t.tweets.Find(query).Sort("-_id").Skip(page * size).Limit(size).All(&results); err != nil {
		return nil, err
	}

	return results, nil
}
