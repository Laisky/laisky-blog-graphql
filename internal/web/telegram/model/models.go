// Package model defines the data model for the blog service.
package model

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// AlertTypes type of alert
type AlertTypes struct {
	ID         primitive.ObjectID `bson:"_id,omitempty" json:"mongo_id"`
	Name       string             `bson:"name" json:"name"`
	PushToken  string             `bson:"push_token" json:"push_token"`
	JoinKey    string             `bson:"join_key" json:"join_key"`
	CreatedAt  time.Time          `bson:"created_at" json:"created_at"`
	ModifiedAt time.Time          `bson:"modified_at" json:"modified_at"`
}

type Users struct {
	ID         primitive.ObjectID `bson:"_id,omitempty" json:"mongo_id"`
	CreatedAt  time.Time          `bson:"created_at" json:"created_at"`
	ModifiedAt time.Time          `bson:"modified_at" json:"modified_at"`
	Name       string             `bson:"name" json:"name"`
	UID        int                `bson:"uid" json:"uid"`
}

type UserAlertRelations struct {
	ID           primitive.ObjectID `bson:"_id,omitempty" json:"mongo_id"`
	CreatedAt    time.Time          `bson:"created_at" json:"created_at"`
	ModifiedAt   time.Time          `bson:"modified_at" json:"modified_at"`
	UserMongoID  primitive.ObjectID `bson:"user_id" json:"user_id"`
	AlertMongoID primitive.ObjectID `bson:"alert_id" json:"alert_id"`
}
