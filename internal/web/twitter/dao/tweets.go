// Package dao implements the Twitter database
package dao

import (
	"laisky-blog-graphql/library/db"

	"gopkg.in/mgo.v2"
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
