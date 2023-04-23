package dao

import (
	"context"

	"github.com/Laisky/laisky-blog-graphql/internal/web/blog/model"
	"github.com/Laisky/laisky-blog-graphql/library/db/mongo"
	mongoLib "go.mongodb.org/mongo-driver/mongo"
)

// Instance dao instance
var Instance *Type

// Initialize initialize dao
func Initialize(ctx context.Context) {
	model.Initialize(ctx)
	Instance = New(model.BlogDB)
}

// Type dao type
type Type struct {
	db mongo.DB
}

// New create new dao
func New(db mongo.DB) *Type {
	return &Type{
		db: db,
	}
}

// PostTagsCol get post tags collection
func (d *Type) PostTagsCol() *mongoLib.Collection {
	return d.db.GetCol("keywords")
}

// GetPostsCol get posts collection
func (d *Type) GetPostsCol() *mongoLib.Collection {
	return d.db.GetCol("posts")
}

// GetUsersCol get users collection
func (d *Type) GetUsersCol() *mongoLib.Collection {
	return d.db.GetCol("users")
}

// GetCategoriesCol get categories collection
func (d *Type) GetCategoriesCol() *mongoLib.Collection {
	return d.db.GetCol("categories")
}

// GetPostSeriesCol get post series collection
func (d *Type) GetPostSeriesCol() *mongoLib.Collection {
	return d.db.GetCol("post_series")
}
