// Package db implements the Twitter database
package db

import (
	"laisky-blog-graphql/library/db"

	"gopkg.in/mgo.v2"
)

const (
	DBName    = "twitter"
	colTweets = "tweets"
	colUsers  = "users"
)

func NewTwitterDB(db *db.DB) *DB {
	return &DB{
		DB: db,
	}
}

type DB struct {
	*db.DB
}

func (db *DB) GetTweetCol() *mgo.Collection {
	return db.GetCol(colTweets)
}
func (db *DB) GetUserCol() *mgo.Collection {
	return db.GetCol(colUsers)
}
