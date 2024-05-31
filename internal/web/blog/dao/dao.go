// Package dao contains all the data access object used in the application.
package dao

import (
	"context"
	"encoding/json"

	"github.com/Laisky/errors/v2"
	glog "github.com/Laisky/go-utils/v4/log"
	mongoLib "go.mongodb.org/mongo-driver/mongo"

	"github.com/Laisky/laisky-blog-graphql/library/db/arweave"
	"github.com/Laisky/laisky-blog-graphql/library/db/mongo"
)

type Arweave interface {
	Upload(ctx context.Context, data []byte,
		opts ...arweave.UploadOption) (fileID string, err error)
}

// Blog dao type
type Blog struct {
	logger  glog.Logger
	db      mongo.DB
	arweave Arweave
}

// New create new dao
func New(logger glog.Logger,
	db mongo.DB,
	arweave *arweave.Akord,
) *Blog {
	return &Blog{
		logger:  logger,
		db:      db,
		arweave: arweave,
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

// SaveToArweave save data to arweave
func (d *Blog) SaveToArweave(ctx context.Context, data any) (fileID string, err error) {
	if d.arweave == nil {
		return "", errors.New("arweave is not enabled")
	}

	payload, err := json.Marshal(data)
	if err != nil {
		return "", errors.Wrap(err, "marshal data")
	}

	// akord do not support content type
	return d.arweave.Upload(ctx, payload) // arweave.WithContentType("application/json"),

}
