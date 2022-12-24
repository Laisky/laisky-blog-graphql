package dao

import (
	"context"

	"github.com/Laisky/laisky-blog-graphql/internal/web/blog/model"
	"github.com/Laisky/laisky-blog-graphql/library/db/mongo"
	mongoLib "go.mongodb.org/mongo-driver/mongo"
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
	db mongo.DB
}

func New(db mongo.DB) *Type {
	return &Type{
		db: db,
	}
}

func (d *Type) GetPostsCol() *mongoLib.Collection {
	return d.db.GetCol(PostColName)
}
func (d *Type) GetUsersCol() *mongoLib.Collection {
	return d.db.GetCol(UserColName)
}
func (d *Type) GetCategoriesCol() *mongoLib.Collection {
	return d.db.GetCol(CategoryColName)
}
func (d *Type) GetPostSeriesCol() *mongoLib.Collection {
	return d.db.GetCol(PostSeriesColName)
}
