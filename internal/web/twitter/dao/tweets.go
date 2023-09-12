// Package dao implements the Twitter database
package dao

import (
	"context"

	"github.com/Laisky/errors/v2"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	mongoLib "go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/Laisky/laisky-blog-graphql/library/db/mongo"
)

const (
	DBName    = "twitter"
	colTweets = "tweets"
	colUsers  = "users"
)

func NewTweets(db mongo.DB) *Tweets {
	return &Tweets{
		DB: db,
	}
}

type Tweets struct {
	mongo.DB
}

func (d *Tweets) GetTweetCol() *mongoLib.Collection {
	return d.GetCol(colTweets)
}
func (d *Tweets) GetUserCol() *mongoLib.Collection {
	return d.GetCol(colUsers)
}

func (d *Tweets) SearchByText(ctx context.Context, text string) (tweetIDs []string, err error) {
	cur, err := d.GetTweetCol().
		Find(ctx,
			bson.M{"text": primitive.Regex{Pattern: text, Options: "i"}},
			options.Find().SetLimit(99),
			options.Find().SetSort(bson.D{bson.E{Key: "created_at", Value: -1}}),
			options.Find().SetProjection(bson.M{"id_str": 1}),
		)
	if err != nil {
		return nil, errors.Wrapf(err, "search text `%s", text)
	}

	var tweets []bson.D
	if err = cur.All(ctx, &tweets); err != nil {
		return nil, errors.Wrap(err, "load tweets")
	}

	for i := range tweets {
		tweetIDs = append(tweetIDs, tweets[i].Map()["id_str"].(string))
	}

	return tweetIDs, nil
}
