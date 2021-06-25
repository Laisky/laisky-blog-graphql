package twitter

import (
	"time"

	"gopkg.in/mgo.v2/bson"
)

type Media struct {
	ID  int64  `bson:"id" json:"id"`
	URL string `bson:"media_url_https" json:"media_url_https"`
}

type Entities struct {
	Media []*Media `bson:"media" json:"media"`
}

type Tweet struct {
	MongoID         bson.ObjectId `bson:"_id" json:"mongo_id"`
	ID              string        `bson:"id_str" json:"id"`
	CreatedAt       *time.Time    `bson:"created_at" json:"created_at"`
	Text            string        `bson:"text" json:"text"`
	Topics          []string      `bson:"topics" json:"topics"`
	User            *User         `bson:"user" json:"user"`
	ReplyToStatusID string        `bson:"in_reply_to_status_id_str" json:"in_reply_to_status_id"`
	Entities        *Entities     `bson:"entities" json:"entities"`
	IsRetweeted     bool          `bson:"retweeted" json:"is_retweeted"`
	RetweetedTweet  *Tweet        `bson:"retweeted_status,omitempty" json:"retweeted_tweet"`
	IsQuoted        bool          `bson:"is_quote_status" json:"is_quote_status"`
	QuotedTweet     *Tweet        `bson:"quoted_status,omitempty" json:"quoted_status"`
	Viewer          []string      `bson:"viewer,omitempty" json:"viewer"`
}

type User struct {
	ID         string `bson:"id_str" json:"id"`
	ScreenName string `bson:"screen_name" json:"screen_name"`
	Name       string `bson:"name" json:"name"`
	Dscription string `bson:"dscription" json:"dscription"`
}
