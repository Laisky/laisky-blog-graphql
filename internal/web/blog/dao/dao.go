// Package dao contains all the data access object used in the application.
package dao

import (
	glog "github.com/Laisky/go-utils/v4/log"
	"github.com/Laisky/laisky-blog-graphql/library/db/mongo"
	mongoLib "go.mongodb.org/mongo-driver/mongo"
)

// Blog dao type
type Blog struct {
	logger glog.Logger
	db     mongo.DB
}

// New create new dao
func New(logger glog.Logger, db mongo.DB) *Blog {
	return &Blog{
		logger: logger,
		db:     db,
	}
}

// PostTagsCol get post tags collection
func (d *Blog) PostTagsCol() *mongoLib.Collection {
	return d.db.GetCol("keywords")
}

// GetPostsCol get posts collection
func (d *Blog) GetPostsCol() *mongoLib.Collection {
	return d.db.GetCol("posts")
}

// GetUsersCol get users collection
func (d *Blog) GetUsersCol() *mongoLib.Collection {
	return d.db.GetCol("users")
}

// GetCategoriesCol get categories collection
func (d *Blog) GetCategoriesCol() *mongoLib.Collection {
	return d.db.GetCol("categories")
}

// GetPostSeriesCol get post series collection
func (d *Blog) GetPostSeriesCol() *mongoLib.Collection {
	return d.db.GetCol("post_series")
}
