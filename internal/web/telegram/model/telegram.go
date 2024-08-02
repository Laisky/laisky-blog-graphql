// Package model defines the data model for the blog service.
package model

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// TelegramNote is the model for telegram notes
type TelegramNote struct {
	ID         primitive.ObjectID `bson:"_id,omitempty" json:"mongo_id"`
	CreatedAt  time.Time          `bson:"created_at" json:"created_at"`
	ModifiedAt time.Time          `bson:"modified_at" json:"modified_at"`
	Content    string             `bson:"content" json:"content"`
	PostID     int                `bson:"post_id" json:"post_id"`
	ArweaveID  string             `bson:"arweave_id" json:"arweave_id"`
}
