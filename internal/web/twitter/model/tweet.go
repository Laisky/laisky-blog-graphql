package model

import (
	"time"

	"github.com/Laisky/errors/v2"
	"github.com/jinzhu/copier"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

type Media struct {
	URL string `bson:"media_url_https" json:"media_url_https"`
}

type Entities struct {
	Media []*Media `bson:"media" json:"media"`
}

type EmbededTweet struct {
	MongoID         primitive.ObjectID `bson:"_id" json:"mongo_id"`
	ID              string             `bson:"id_str" json:"id"`
	CreatedAt       string             `bson:"created_at" json:"created_at"`
	Text            string             `bson:"full_text" json:"text"`
	Topics          []string           `bson:"topics" json:"topics"`
	User            *User              `bson:"user" json:"user"`
	ReplyToStatusID string             `bson:"in_reply_to_status_id_str" json:"in_reply_to_status_id"`
	Entities        *Entities          `bson:"entities" json:"entities"`
	IsRetweeted     bool               `bson:"retweeted" json:"is_retweeted"`
	RetweetedTweet  *EmbededTweet      `bson:"retweeted_status,omitempty" json:"retweeted_tweet"`
	IsQuoted        bool               `bson:"is_quote_status" json:"is_quote_status"`
	QuotedTweet     *EmbededTweet      `bson:"quoted_status,omitempty" json:"quoted_status"`
	Viewer          []int64            `bson:"viewer,omitempty" json:"viewer"`
}

func (t *EmbededTweet) ToTweet() (tweet *Tweet, err error) {
	tweet = new(Tweet)
	if err = copier.Copy(tweet, t); err != nil {
		return nil, errors.Wrap(err, "copy")
	}

	// Wed Mar 09 01:18:01 +0000 2022
	tweet.CreatedAt, err = time.Parse(time.RubyDate, t.CreatedAt)
	if err != nil {
		return nil, errors.Wrapf(err, "parse tweet created_at %q", t.CreatedAt)
	}

	return tweet, nil
}

type Tweet struct {
	MongoID         primitive.ObjectID `bson:"_id" json:"mongo_id"`
	ID              string             `bson:"id_str" json:"id"`
	CreatedAt       time.Time          `bson:"created_at" json:"created_at"`
	Text            string             `bson:"text" json:"text"`
	Topics          []string           `bson:"topics" json:"topics"`
	User            *User              `bson:"user" json:"user"`
	ReplyToStatusID string             `bson:"in_reply_to_status_id_str" json:"in_reply_to_status_id"`
	Entities        *Entities          `bson:"entities" json:"entities"`
	IsRetweeted     bool               `bson:"retweeted" json:"is_retweeted"`
	RetweetedTweet  *EmbededTweet      `bson:"retweeted_status,omitempty" json:"retweeted_tweet"`
	IsQuoted        bool               `bson:"is_quote_status" json:"is_quote_status"`
	QuotedTweet     *EmbededTweet      `bson:"quoted_status,omitempty" json:"quoted_status"`
	Viewer          []int64            `bson:"viewer,omitempty" json:"viewer"`
}
