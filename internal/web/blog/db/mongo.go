package db

import (
	"gopkg.in/mgo.v2"

	"laisky-blog-graphql/library/db"
)

const (
	DBName            = "blog"
	PostColName       = "posts"
	UserColName       = "users"
	PostSeriesColName = "post_series"
	CategoryColName   = "categories"
)

type DB struct {
	dbcli *db.DB
}

func NewDB(dbcli *db.DB) *DB {
	return &DB{
		dbcli: dbcli,
	}
}

func (db *DB) GetPostsCol() *mgo.Collection {
	return db.dbcli.GetCol(PostColName)
}
func (db *DB) GetUsersCol() *mgo.Collection {
	return db.dbcli.GetCol(UserColName)
}
func (db *DB) GetCategoriesCol() *mgo.Collection {
	return db.dbcli.GetCol(CategoryColName)
}
func (db *DB) GetPostSeriesCol() *mgo.Collection {
	return db.dbcli.GetCol(PostSeriesColName)
}
