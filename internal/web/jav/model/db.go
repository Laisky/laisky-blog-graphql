// Package model is a package for defining the data model for the jav service.
package model

import (
	"context"

	"github.com/Laisky/errors/v2"

	"github.com/Laisky/laisky-blog-graphql/library/db/mongo"
)

// NewJavDB create new db
func NewJavDB(ctx context.Context, info mongo.DialInfo) (db mongo.DB, err error) {
	db, err = mongo.NewDB(ctx, info)
	if err != nil {
		return nil, errors.Wrap(err, "dial jav db")
	}

	return db, nil
}
