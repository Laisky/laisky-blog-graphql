// Package dao telegram
package dao

import (
	"context"
	"testing"

	"github.com/Laisky/laisky-blog-graphql/library/config"
	"github.com/Laisky/laisky-blog-graphql/library/db/mongo"
)

func TestType_LoadUserByUID(t *testing.T) {
	ctx := context.Background()
	config.LoadTest()
	mongo.NewDB(ctx, mongo.DialInfo{})
}
