package dao

import (
	"context"

	"gopkg.in/mgo.v2"

	"laisky-blog-graphql/internal/web/blog/model"
	"laisky-blog-graphql/library/db"
)

const (
	DBName            = "blog"
	PostColName       = "posts"
	UserColName       = "users"
	PostSeriesColName = "post_series"
	CategoryColName   = "categories"
)

var Instance *Type

func Initialize(ctx context.Context) {
	model.Initialize(ctx)
	Instance = New(model.BlogDB)
}

type Type struct {
	db *db.DB
}

func New(db *db.DB) *Type {
	return &Type{
		db: db,
	}
}

func (d *Type) GetPostsCol() *mgo.Collection {
	return d.db.GetCol(PostColName)
}
func (d *Type) GetUsersCol() *mgo.Collection {
	return d.db.GetCol(UserColName)
}
func (d *Type) GetCategoriesCol() *mgo.Collection {
	return d.db.GetCol(CategoryColName)
}
func (d *Type) GetPostSeriesCol() *mgo.Collection {
	return d.db.GetCol(PostSeriesColName)
}
