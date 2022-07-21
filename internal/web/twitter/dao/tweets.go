// Package dao implements the Twitter database
package dao

import (
	"laisky-blog-graphql/library/db"

	"github.com/pkg/errors"
	"gopkg.in/mgo.v2"
	"gopkg.in/mgo.v2/bson"
)

const (
	DBName    = "twitter"
	colTweets = "tweets"
	colUsers  = "users"
)

func NewTweets(db *db.DB) *Tweets {
	return &Tweets{
		DB: db,
	}
}

type Tweets struct {
	*db.DB
}

func (d *Tweets) GetTweetCol() *mgo.Collection {
	return d.GetCol(colTweets)
}
func (d *Tweets) GetUserCol() *mgo.Collection {
	return d.GetCol(colUsers)
}

func (d *Tweets) SearchByText(text string) (tweetIDs []string, err error) {
	if err = d.GetTweetCol().
		Find(bson.M{"text": bson.RegEx{Pattern: text, Options: "i"}}).
		Limit(99).
		Sort("-created_at").
		Select(bson.M{"id_str": 1}).
		All(&tweetIDs); err != nil {
		return nil, errors.Wrapf(err, "search text `%s", text)
	}

	return tweetIDs, nil
}
